package dockerui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

// Applications: the containers of one Compose project seen as one block.
// Everything is read from the labels Compose puts on its containers; no
// compose file is opened and no `docker compose` command is run. A stack
// action is a sequence of the per-container actions of this package: it
// never creates, removes, rebuilds nor pulls anything.

const (
	labelProject     = "com.docker.compose.project"
	labelService     = "com.docker.compose.service"
	labelWorkingDir  = "com.docker.compose.project.working_dir"
	labelConfigFiles = "com.docker.compose.project.config_files"
	labelDependsOn   = "com.docker.compose.depends_on"
)

// Stack states.
const (
	stackRunning = "running" // every container runs
	stackPartial = "partial" // some run, some do not
	stackStopped = "stopped" // none runs
)

// Outcomes of one container in a stack action.
const (
	resultDone      = "done"
	resultUnchanged = "unchanged" // already in the requested state
	resultFailed    = "failed"
	resultSkipped   = "skipped" // not attempted: an earlier container failed
)

var (
	errStackNotFound = errors.New("application introuvable")

	// stackPattern is the shape of a Compose project name taken from a URL.
	stackPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// hubHostname is the default hostname of a container: its short ID.
	hubHostname = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// Stack is one Compose project.
type Stack struct {
	Name        string   `json:"name"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files"`
	// State is running, partial or stopped.
	State   string `json:"state"`
	Running int    `json:"running"`
	Total   int    `json:"total"`
	// Services lists the containers in start order: dependencies first.
	Services []Container `json:"services"`
	// Protected explains why the stack cannot be stopped nor restarted.
	Protected string `json:"protected,omitempty"`
}

// StacksResponse is the payload of GET /api/docker/stacks.
type StacksResponse struct {
	Available bool    `json:"available"`
	Error     string  `json:"error,omitempty"`
	Stacks    []Stack `json:"stacks"`
	// Standalone lists the containers that belong to no Compose project.
	Standalone []Container `json:"standalone"`
	// Routes says which domains and published ports lead to which container.
	Routes      []Route `json:"routes"`
	CollectedAt int64   `json:"collected_at"`
}

// StackActionResult tells what happened to one container of the stack.
type StackActionResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
	// Status is done, unchanged, failed or skipped.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// StackActionResponse answers start, stop and restart of a stack. OK is
// false when a container failed: Results then says which ones succeeded,
// which one failed and which were not attempted.
type StackActionResponse struct {
	Stack   string              `json:"stack"`
	Action  string              `json:"action"`
	OK      bool                `json:"ok"`
	Results []StackActionResult `json:"results"`
}

// hubContainerID returns the short ID of the container the hub runs in, read
// from the hostname Docker gives a container by default, or "".
func hubContainerID() string {
	name, err := os.Hostname()
	if err != nil || !hubHostname.MatchString(name) {
		return ""
	}
	return name
}

// dependsOn reads com.docker.compose.depends_on: "db:service_healthy:false,cache:service_started:true".
func dependsOn(label string) []string {
	var out []string
	for _, item := range strings.Split(label, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(item), ":")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// stackOrder sorts the containers of one stack in start order: a service
// comes after the services it depends on, and otherwise services follow
// their names, then container names. Dependencies on a service the stack
// does not have are ignored, and a cycle falls back to the name order.
func stackOrder(members []apiContainer) []apiContainer {
	byService := make(map[string][]apiContainer)
	for _, c := range members {
		service := c.Labels[labelService]
		byService[service] = append(byService[service], c)
	}
	services := make([]string, 0, len(byService))
	for service, list := range byService {
		services = append(services, service)
		sort.SliceStable(list, func(i, j int) bool {
			a, b := containerName(list[i].Names, list[i].ID), containerName(list[j].Names, list[j].ID)
			if a != b {
				return a < b
			}
			return list[i].ID < list[j].ID
		})
	}
	sort.Strings(services)

	needs := make(map[string]map[string]bool, len(services))
	for _, service := range services {
		needs[service] = map[string]bool{}
		for _, c := range byService[service] {
			for _, dependency := range dependsOn(c.Labels[labelDependsOn]) {
				if _, known := byService[dependency]; known && dependency != service {
					needs[service][dependency] = true
				}
			}
		}
	}
	out := make([]apiContainer, 0, len(members))
	placed := make(map[string]bool, len(services))
	for len(placed) < len(services) {
		progressed := false
		for _, service := range services {
			if placed[service] {
				continue
			}
			ready := true
			for dependency := range needs[service] {
				if !placed[dependency] {
					ready = false
					break
				}
			}
			if ready {
				placed[service] = true
				out = append(out, byService[service]...)
				progressed = true
				break // restart from the first name, so the order is stable
			}
		}
		if !progressed { // a cycle: the rest goes in name order
			for _, service := range services {
				if !placed[service] {
					placed[service] = true
					out = append(out, byService[service]...)
				}
			}
		}
	}
	return out
}

// stackTargets tells whether an action applies to a container in that state.
// Start takes the containers that do not run; stop and restart those that do.
// Restart leaves stopped containers alone, so a finished one-shot job is not
// run again.
func stackTargets(action, state string) bool {
	active := state == "running" || state == "restarting" || state == "paused"
	if action == "start" {
		return state == "created" || state == "exited" || state == "dead"
	}
	return active
}

// stackMembers returns the containers of a project, in listing order.
func stackMembers(list []apiContainer, project string) []apiContainer {
	var out []apiContainer
	for _, c := range list {
		if c.Labels[labelProject] == project {
			out = append(out, c)
		}
	}
	return out
}

// hubIn returns the container of the list the hub itself runs in, if any.
func (s *Service) hubIn(members []apiContainer) (apiContainer, bool) {
	self := ""
	if s.self != nil {
		self = s.self()
	}
	if !hubHostname.MatchString(self) && !idPattern.MatchString(self) {
		return apiContainer{}, false
	}
	for _, c := range members {
		if strings.HasPrefix(c.ID, self) {
			return c, true
		}
	}
	return apiContainer{}, false
}

// protectedMessage is the refusal for a stack that contains the hub: Atlas
// would stop itself in the middle of the sequence and leave the stack half
// stopped, with nobody to report it.
func protectedMessage(c apiContainer) string {
	return "cette application contient Atlas lui-même (conteneur « " + containerName(c.Names, c.ID) +
		" ») : l’arrêter ou la redémarrer d’ici couperait l’action en cours de route"
}

func buildStack(name string, members []apiContainer) Stack {
	ordered := stackOrder(members)
	stack := Stack{Name: name, ConfigFiles: []string{}, Total: len(ordered), Services: make([]Container, 0, len(ordered))}
	for _, c := range ordered {
		if stack.WorkingDir == "" {
			stack.WorkingDir = c.Labels[labelWorkingDir]
		}
		if len(stack.ConfigFiles) == 0 {
			for _, file := range strings.Split(c.Labels[labelConfigFiles], ",") {
				if file = strings.TrimSpace(file); file != "" {
					stack.ConfigFiles = append(stack.ConfigFiles, file)
				}
			}
		}
		if c.State == "running" {
			stack.Running++
		}
		stack.Services = append(stack.Services, containerView(c, apiInspect{}))
	}
	switch {
	case stack.Running == 0:
		stack.State = stackStopped
	case stack.Running == stack.Total:
		stack.State = stackRunning
	default:
		stack.State = stackPartial
	}
	return stack
}

// Stacks groups the containers by Compose project and lists the routes.
func (s *Service) Stacks(ctx context.Context) StacksResponse {
	response := StacksResponse{Stacks: []Stack{}, Standalone: []Container{}, Routes: []Route{}, CollectedAt: s.now().UnixMilli()}
	if err := s.usable(); err != nil {
		response.Error = err.Error()
		return response
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	list, err := s.client.listContainers(listCtx)
	cancel()
	if err != nil {
		response.Error = err.Error()
		return response
	}
	response.Available = true

	projects := make(map[string][]apiContainer)
	for _, c := range list {
		view := containerView(c, apiInspect{})
		response.Routes = append(response.Routes, containerRoutes(view, c.Labels)...)
		if project := c.Labels[labelProject]; project != "" {
			projects[project] = append(projects[project], c)
			continue
		}
		response.Standalone = append(response.Standalone, view)
	}
	for name, members := range projects {
		stack := buildStack(name, members)
		if hub, found := s.hubIn(members); found {
			stack.Protected = protectedMessage(hub)
		}
		response.Stacks = append(response.Stacks, stack)
	}
	sort.Slice(response.Stacks, func(i, j int) bool { return response.Stacks[i].Name < response.Stacks[j].Name })
	sort.SliceStable(response.Standalone, func(i, j int) bool { return response.Standalone[i].Name < response.Standalone[j].Name })
	sortRoutes(response.Routes)
	return response
}

// StackAction starts, stops or restarts the containers of a Compose project,
// one after the other: dependencies first for start and restart, last for
// stop. It stops at the first failure and reports every container.
func (s *Service) StackAction(ctx context.Context, action, project string) (StackActionResponse, error) {
	response := StackActionResponse{Action: action, Results: []StackActionResult{}}
	switch action {
	case "start", "stop", "restart":
	default:
		return response, errBadRequest("action inconnue")
	}
	if !stackPattern.MatchString(project) {
		return response, errBadRequest("nom d’application invalide")
	}
	if err := s.usable(); err != nil {
		return response, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	list, err := s.client.listContainers(listCtx)
	cancel()
	if err != nil {
		return response, err
	}
	members := stackMembers(list, project)
	if len(members) == 0 {
		return response, errStackNotFound
	}
	response.Stack = project
	if hub, found := s.hubIn(members); found && action != "start" {
		return response, &conflictError{message: protectedMessage(hub)}
	}
	if !s.lockStack(project) {
		return response, &conflictError{message: "une action est déjà en cours sur cette application"}
	}
	defer s.unlockStack(project)

	ordered := stackOrder(members)
	if action == "stop" {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}
	response.OK = true
	for _, c := range ordered {
		result := StackActionResult{ID: c.ID, Name: containerName(c.Names, c.ID), Service: c.Labels[labelService], Status: resultUnchanged}
		switch {
		case !stackTargets(action, c.State):
		case !response.OK:
			result.Status = resultSkipped
		case !idPattern.MatchString(c.ID):
			result.Status, result.Error = resultFailed, errInvalidID.Error()
			response.OK = false
		default:
			done, err := s.act(ctx, action, c)
			switch {
			case err != nil:
				_, result.Error = errorStatus(err)
				result.Status = resultFailed
				response.OK = false
			case done.Changed:
				result.Status = resultDone
			}
		}
		response.Results = append(response.Results, result)
	}
	return response, nil
}

func (s *Service) lockStack(project string) bool {
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	if s.stackBusy[project] {
		return false
	}
	if s.stackBusy == nil {
		s.stackBusy = make(map[string]bool)
	}
	s.stackBusy[project] = true
	return true
}

func (s *Service) unlockStack(project string) {
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	delete(s.stackBusy, project)
}

// Handlers.

func (s *Service) registerStacks(read, mutate chi.Router) {
	read.Get("/docker/stacks", s.handleStacks)
	for _, action := range []string{"start", "stop", "restart"} {
		mutate.Post("/docker/stacks/{project}/"+action, s.handleStackAction(action))
	}
}

func (s *Service) handleStacks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Stacks(r.Context()))
}

// handleStackAction answers 200 as soon as the stack was found and allowed,
// even when a container failed: the body then has ok=false and one result
// per container, which an error status could not carry.
func (s *Service) handleStackAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := s.StackAction(r.Context(), action, chi.URLParam(r, "project"))
		if err != nil {
			status, message := errorStatus(err)
			// result.Stack is only set once Docker listed the project.
			s.audit(r, "stack-"+action, "", "").
				WithFields(logrus.Fields{"stack": result.Stack, "status": status, "error": message}).
				Warn("docker stack action refused or failed")
			writeError(w, status, message)
			return
		}
		for _, item := range result.Results {
			entry := s.audit(r, "stack-"+action, item.Name, item.ID).
				WithFields(logrus.Fields{"stack": result.Stack, "result": item.Status})
			if item.Status == resultFailed {
				entry.WithField("error", item.Error).Warn("docker stack action failed on a container")
			} else {
				entry.Info("docker stack action")
			}
		}
		writeJSON(w, http.StatusOK, result)
	}
}
