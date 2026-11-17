package dockerui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

// Images, volumes and container creation: the rest of what Docker Desktop
// shows next to the containers. Like the container routes, nothing here
// forces: Docker refuses to delete an image or a volume still in use.

const (
	pullTimeout     = 10 * time.Minute
	pruneTimeout    = 2 * time.Minute
	createTimeout   = 60 * time.Second
	volumeDFTimeout = 20 * time.Second
	volumeDFTTL     = time.Minute
	pullLimit       = 32 << 20
	bodyLimit       = 64 << 10
	maxPorts        = 32
	maxEnv          = 64
	maxEnvLen       = 4096
	maxMounts       = 16
)

var (
	errImageNotFound  = errors.New("image introuvable")
	errVolumeNotFound = errors.New("volume introuvable")

	// imageRefPattern is a registry reference: name, optional tag or digest.
	imageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	volumePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	namePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	envPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=[^\x00\r\n]*$`)
	mountPattern    = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]+$`)
	anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Engine API payloads.

type apiImage struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  int64    `json:"Created"`
	Size     int64    `json:"Size"`
}

type apiVolume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
}

// Views sent to the browser.

// Image is one row of the Images tab.
type Image struct {
	ID      string   `json:"id"`
	ShortID string   `json:"short_id"`
	Tags    []string `json:"tags"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
	// Containers names the containers created from the image, running or not.
	Containers []string `json:"containers"`
}

// ImagesResponse is the payload of GET /api/docker/images.
type ImagesResponse struct {
	Images      []Image `json:"images"`
	TotalSize   int64   `json:"total_size"`
	CollectedAt int64   `json:"collected_at"`
}

// Volume is one row of the Volumes tab.
type Volume struct {
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	CreatedAt string `json:"created_at,omitempty"`
	// Size is nil while Docker has not measured the volume.
	Size      *int64 `json:"size"`
	Anonymous bool   `json:"anonymous,omitempty"`
	Project   string `json:"compose_project,omitempty"`
	// Containers names the containers that mount the volume, running or not.
	Containers []string `json:"containers"`
}

// VolumesResponse is the payload of GET /api/docker/volumes.
type VolumesResponse struct {
	Volumes     []Volume `json:"volumes"`
	CollectedAt int64    `json:"collected_at"`
}

// ResourceResponse answers a change to an image or a volume.
type ResourceResponse struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	// Detail is Docker's last status line of a pull, or what a prune freed.
	Detail string `json:"detail,omitempty"`
}

// CreateRequest is the body of POST /api/docker/containers: the options of
// Docker Desktop's "Run" dialog. Host paths cannot be mounted, only named
// volumes, and ports are published on 127.0.0.1 unless HostIP says otherwise.
type CreateRequest struct {
	Image   string        `json:"image"`
	Name    string        `json:"name"`
	Ports   []CreatePort  `json:"ports"`
	Env     []string      `json:"env"`
	Volumes []CreateMount `json:"volumes"`
	Restart string        `json:"restart"`
	// Start is true unless the body says false.
	Start *bool `json:"start"`
}

// CreatePort publishes a container port on the host.
type CreatePort struct {
	HostIP    string `json:"host_ip"`
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Proto     string `json:"proto"`
}

// CreateMount mounts a named volume at an absolute path of the container.
type CreateMount struct {
	Volume string `json:"volume"`
	Path   string `json:"path"`
}

// volumeSizes caches the slow GET /system/df.
type volumeSizes struct {
	mu      sync.Mutex
	sizes   map[string]int64
	fetched time.Time
	busy    bool
}

func (s *Service) registerResources(read, mutate chi.Router) {
	mutate.Post("/docker/containers", s.handleCreate)
	read.Get("/docker/images", s.handleImages)
	mutate.Post("/docker/images/pull", s.handlePull)
	mutate.Post("/docker/images/prune", s.handlePrune)
	mutate.Delete("/docker/images/{id}", s.handleRemoveImage)
	read.Get("/docker/volumes", s.handleVolumes)
	mutate.Post("/docker/volumes", s.handleCreateVolume)
	mutate.Delete("/docker/volumes/{name}", s.handleRemoveVolume)
}

// Images.

func (s *Service) listImages(ctx context.Context) ([]apiImage, error) {
	callCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var out []apiImage
	err := s.client.getJSON(callCtx, "/images/json", nil, listLimit, &out)
	return out, err
}

func realTags(tags []string) []string {
	out := []string{}
	for _, tag := range tags {
		if tag != "" && tag != "<none>:<none>" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// Images lists the local images with the containers created from each.
func (s *Service) Images(ctx context.Context) (ImagesResponse, error) {
	response := ImagesResponse{Images: []Image{}, CollectedAt: s.now().UnixMilli()}
	if err := s.usable(); err != nil {
		return response, err
	}
	images, err := s.listImages(ctx)
	if err != nil {
		return response, err
	}
	users := map[string][]string{}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	containers, err := s.client.listContainers(listCtx)
	cancel()
	if err != nil {
		return response, err
	}
	for _, c := range containers {
		users[c.ImageID] = append(users[c.ImageID], containerName(c.Names, c.ID))
	}
	for _, image := range images {
		names := users[image.ID]
		if names == nil {
			names = []string{}
		}
		sort.Strings(names)
		response.Images = append(response.Images, Image{
			ID: image.ID, ShortID: shortID(strings.TrimPrefix(image.ID, "sha256:")), Tags: realTags(image.RepoTags),
			Size: image.Size, Created: image.Created, Containers: names,
		})
		response.TotalSize += image.Size
	}
	sort.SliceStable(response.Images, func(i, j int) bool {
		a, b := response.Images[i], response.Images[j]
		if (len(a.Tags) == 0) != (len(b.Tags) == 0) {
			return len(a.Tags) > 0
		}
		if len(a.Tags) > 0 && a.Tags[0] != b.Tags[0] {
			return a.Tags[0] < b.Tags[0]
		}
		return a.Created > b.Created
	})
	return response, nil
}

// resolveImage finds the image whose ID starts with id in a fresh listing.
func (s *Service) resolveImage(ctx context.Context, id string) (apiImage, error) {
	id = strings.TrimPrefix(id, "sha256:")
	if !idPattern.MatchString(id) {
		return apiImage{}, errBadRequest("identifiant d’image invalide")
	}
	if err := s.usable(); err != nil {
		return apiImage{}, err
	}
	images, err := s.listImages(ctx)
	if err != nil {
		return apiImage{}, err
	}
	var found *apiImage
	for i := range images {
		if strings.HasPrefix(strings.TrimPrefix(images[i].ID, "sha256:"), id) {
			if found != nil {
				return apiImage{}, errBadRequest("identifiant d’image ambigu")
			}
			found = &images[i]
		}
	}
	if found == nil {
		return apiImage{}, errImageNotFound
	}
	return *found, nil
}

func imageLabel(image apiImage) string {
	if tags := realTags(image.RepoTags); len(tags) > 0 {
		return tags[0]
	}
	return shortID(strings.TrimPrefix(image.ID, "sha256:"))
}

// RemoveImage deletes an image without force: Docker refuses while a
// container, even stopped, was created from it. An image with several tags
// loses them one by one; the last one deletes its layers.
func (s *Service) RemoveImage(ctx context.Context, id string) (ResourceResponse, error) {
	image, err := s.resolveImage(ctx, id)
	if err != nil {
		return ResourceResponse{}, err
	}
	result := ResourceResponse{Name: imageLabel(image), Action: "delete-image"}
	callCtx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	targets := realTags(image.RepoTags)
	if len(targets) < 2 {
		targets = []string{image.ID}
	}
	for _, target := range targets {
		if _, err := s.client.send(callCtx, http.MethodDelete, "/images/"+target); err != nil {
			return result, err
		}
	}
	return result, nil
}

// splitImageRef separates the tag or digest the Engine API wants apart.
func splitImageRef(ref string) (name, tag string, err error) {
	if !imageRefPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") {
		return "", "", errBadRequest("nom d’image invalide (exemple : nginx:1.27 ou ghcr.io/org/app:latest)")
	}
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[:at], ref[at+1:], nil
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		return ref[:colon], ref[colon+1:], nil
	}
	return ref, "latest", nil
}

// Pull downloads a public image and waits for the end of the download.
func (s *Service) Pull(ctx context.Context, ref string) (ResourceResponse, error) {
	result := ResourceResponse{Name: ref, Action: "pull"}
	if err := s.usable(); err != nil {
		return result, err
	}
	name, tag, err := splitImageRef(strings.TrimSpace(ref))
	if err != nil {
		return result, err
	}
	result.Name = name + ":" + tag
	if strings.HasPrefix(tag, "sha256:") {
		result.Name = name + "@" + tag
	}
	callCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	resp, err := s.client.do(callCtx, http.MethodPost, "/images/create", url.Values{"fromImage": {name}, "tag": {tag}})
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, apiErrorFrom(resp)
	}
	// Docker answers 200 then streams the progress: a failure is a line
	// with an "error" field.
	decoder := json.NewDecoder(io.LimitReader(resp.Body, pullLimit))
	for {
		var line struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if callCtx.Err() != nil {
				return result, errTimeout
			}
			return result, fmt.Errorf("téléchargement interrompu : %w", err)
		}
		if line.Error != "" {
			return result, &conflictError{message: "Docker refuse : " + cleanLogText(line.Error)}
		}
		if line.Status != "" {
			result.Detail = cleanLogText(line.Status)
		}
	}
	return result, nil
}

// PruneImages deletes the dangling images: untagged layers left by rebuilds.
func (s *Service) PruneImages(ctx context.Context) (ResourceResponse, error) {
	result := ResourceResponse{Name: "images sans étiquette", Action: "prune-images"}
	if err := s.usable(); err != nil {
		return result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	resp, err := s.client.do(callCtx, http.MethodPost, "/images/prune", url.Values{"filters": {`{"dangling":["true"]}`}})
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, apiErrorFrom(resp)
	}
	var out struct {
		ImagesDeleted  []json.RawMessage `json:"ImagesDeleted"`
		SpaceReclaimed int64             `json:"SpaceReclaimed"`
	}
	body, err := readLimited(resp.Body, listLimit)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return result, fmt.Errorf("réponse Docker illisible : %w", err)
	}
	result.Detail = strconv.FormatInt(out.SpaceReclaimed, 10)
	return result, nil
}

// Volumes.

func (s *Service) listVolumes(ctx context.Context) ([]apiVolume, error) {
	callCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var out struct {
		Volumes []apiVolume `json:"Volumes"`
	}
	err := s.client.getJSON(callCtx, "/volumes", nil, listLimit, &out)
	return out.Volumes, err
}

// sizes returns the cached volume sizes and refreshes them in the
// background: Docker walks every volume to answer, which can take seconds.
func (s *Service) sizes() map[string]int64 {
	v := &s.volumes
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.busy && s.now().Sub(v.fetched) > volumeDFTTL {
		v.busy = true
		go s.refreshSizes()
	}
	return v.sizes
}

func (s *Service) refreshSizes() {
	ctx, cancel := context.WithTimeout(context.Background(), volumeDFTimeout)
	defer cancel()
	var out struct {
		Volumes []struct {
			Name      string `json:"Name"`
			UsageData *struct {
				Size int64 `json:"Size"`
			} `json:"UsageData"`
		} `json:"Volumes"`
	}
	err := s.client.getJSON(ctx, "/system/df", url.Values{"type": {"volume"}}, listLimit, &out)
	v := &s.volumes
	v.mu.Lock()
	defer v.mu.Unlock()
	v.busy = false
	v.fetched = s.now()
	if err != nil {
		// Sizes stay unknown; the next listing after the TTL tries again.
		s.logger.WithError(err).Debug("docker volume sizes unavailable")
		return
	}
	sizes := make(map[string]int64, len(out.Volumes))
	for _, volume := range out.Volumes {
		if volume.UsageData != nil && volume.UsageData.Size >= 0 {
			sizes[volume.Name] = volume.UsageData.Size
		}
	}
	v.sizes = sizes
}

func (s *Service) forgetSizes() {
	s.volumes.mu.Lock()
	s.volumes.fetched = time.Time{}
	s.volumes.mu.Unlock()
}

// Volumes lists the volumes with their size and the containers mounting them.
func (s *Service) Volumes(ctx context.Context) (VolumesResponse, error) {
	response := VolumesResponse{Volumes: []Volume{}, CollectedAt: s.now().UnixMilli()}
	if err := s.usable(); err != nil {
		return response, err
	}
	volumes, err := s.listVolumes(ctx)
	if err != nil {
		return response, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	containers, err := s.client.listContainers(listCtx)
	cancel()
	if err != nil {
		return response, err
	}
	users := map[string][]string{}
	for _, c := range containers {
		for _, mount := range c.Mounts {
			if mount.Type == "volume" && mount.Name != "" {
				users[mount.Name] = append(users[mount.Name], containerName(c.Names, c.ID))
			}
		}
	}
	sizes := s.sizes()
	for _, volume := range volumes {
		names := users[volume.Name]
		if names == nil {
			names = []string{}
		}
		sort.Strings(names)
		view := Volume{
			Name: volume.Name, Driver: volume.Driver, CreatedAt: dockerTime(volume.CreatedAt),
			Anonymous: anonymousVolume.MatchString(volume.Name), Project: volume.Labels["com.docker.compose.project"],
			Containers: names,
		}
		if size, ok := sizes[volume.Name]; ok {
			view.Size = &size
		}
		response.Volumes = append(response.Volumes, view)
	}
	sort.SliceStable(response.Volumes, func(i, j int) bool {
		a, b := response.Volumes[i], response.Volumes[j]
		if a.Anonymous != b.Anonymous {
			return !a.Anonymous
		}
		return a.Name < b.Name
	})
	return response, nil
}

// CreateVolume creates an empty local volume.
func (s *Service) CreateVolume(ctx context.Context, name string) (ResourceResponse, error) {
	result := ResourceResponse{Name: name, Action: "create-volume"}
	if err := s.usable(); err != nil {
		return result, err
	}
	if !volumePattern.MatchString(name) {
		return result, errBadRequest("nom de volume invalide (lettres, chiffres, . _ -)")
	}
	volumes, err := s.listVolumes(ctx)
	if err != nil {
		return result, err
	}
	for _, volume := range volumes {
		if volume.Name == name {
			return result, &conflictError{message: "un volume porte déjà ce nom"}
		}
	}
	body, _ := json.Marshal(map[string]string{"Name": name})
	callCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	resp, err := s.client.doBody(callCtx, http.MethodPost, "/volumes/create", nil, body)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, apiErrorFrom(resp)
	}
	s.forgetSizes()
	return result, nil
}

// RemoveVolume deletes a volume and its data, without force: Docker refuses
// while a container, even stopped, mounts it.
func (s *Service) RemoveVolume(ctx context.Context, name string) (ResourceResponse, error) {
	result := ResourceResponse{Name: name, Action: "delete-volume"}
	if err := s.usable(); err != nil {
		return result, err
	}
	if !volumePattern.MatchString(name) {
		return result, errBadRequest("nom de volume invalide")
	}
	// Only a name Docker itself listed reaches the delete call.
	volumes, err := s.listVolumes(ctx)
	if err != nil {
		return result, err
	}
	found := false
	for _, volume := range volumes {
		found = found || volume.Name == name
	}
	if !found {
		return result, errVolumeNotFound
	}
	callCtx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	if _, err := s.client.send(callCtx, http.MethodDelete, "/volumes/"+name); err != nil {
		return result, err
	}
	s.forgetSizes()
	return result, nil
}

// Container creation.

func (req CreateRequest) validate() (name string, body []byte, err error) {
	if _, _, err := splitImageRef(req.Image); err != nil {
		return "", nil, err
	}
	if req.Name != "" && !namePattern.MatchString(req.Name) {
		return "", nil, errBadRequest("nom de conteneur invalide (lettres, chiffres, . _ -)")
	}
	if len(req.Ports) > maxPorts || len(req.Env) > maxEnv || len(req.Volumes) > maxMounts {
		return "", nil, errBadRequest("trop de ports, de variables ou de volumes")
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, port := range req.Ports {
		proto := port.Proto
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			return "", nil, errBadRequest("protocole de port invalide (tcp ou udp)")
		}
		if port.Host < 1 || port.Host > 65535 || port.Container < 1 || port.Container > 65535 {
			return "", nil, errBadRequest("un port doit être compris entre 1 et 65535")
		}
		ip := port.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		if net.ParseIP(ip) == nil {
			return "", nil, errBadRequest("adresse d’écoute invalide")
		}
		key := fmt.Sprintf("%d/%s", port.Container, proto)
		exposed[key] = struct{}{}
		bindings[key] = append(bindings[key], map[string]string{"HostIp": ip, "HostPort": strconv.Itoa(port.Host)})
	}
	for _, entry := range req.Env {
		if len(entry) > maxEnvLen || !envPattern.MatchString(entry) {
			return "", nil, errBadRequest("variable d’environnement invalide (format NOM=valeur)")
		}
	}
	binds := []string{}
	for _, mount := range req.Volumes {
		if !volumePattern.MatchString(mount.Volume) {
			return "", nil, errBadRequest("nom de volume invalide")
		}
		if !mountPattern.MatchString(mount.Path) || path.Clean(mount.Path) != mount.Path || mount.Path == "/" {
			return "", nil, errBadRequest("chemin de montage invalide (chemin absolu dans le conteneur)")
		}
		binds = append(binds, mount.Volume+":"+mount.Path)
	}
	restart := req.Restart
	switch restart {
	case "":
		restart = "no"
	case "no", "always", "unless-stopped", "on-failure":
	default:
		return "", nil, errBadRequest("politique de redémarrage invalide")
	}
	env := req.Env
	if env == nil {
		env = []string{}
	}
	body, err = json.Marshal(map[string]any{
		"Image": req.Image, "Env": env, "ExposedPorts": exposed,
		"HostConfig": map[string]any{
			"PortBindings": bindings, "Binds": binds, "RestartPolicy": map[string]string{"Name": restart},
		},
	})
	return req.Name, body, err
}

// Create creates a container from a local image and starts it.
func (s *Service) Create(ctx context.Context, req CreateRequest) (ActionResponse, error) {
	result := ActionResponse{Name: req.Name, Action: "create"}
	if err := s.usable(); err != nil {
		return result, err
	}
	req.Image, req.Name = strings.TrimSpace(req.Image), strings.TrimSpace(req.Name)
	name, body, err := req.validate()
	if err != nil {
		return result, err
	}
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	resp, err := s.client.doBody(callCtx, http.MethodPost, "/containers/create", query, body)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorLimit))
		return result, &conflictError{message: "image absente du serveur : télécharge-la d’abord dans l’onglet Images"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, apiErrorFrom(resp)
	}
	var created struct {
		ID string `json:"Id"`
	}
	raw, err := readLimited(resp.Body, infoLimit)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &created); err != nil || !idPattern.MatchString(created.ID) {
		return result, errors.New("réponse Docker illisible à la création du conteneur")
	}
	result.ID, result.Changed = created.ID, true
	if result.Name == "" {
		result.Name = shortID(created.ID)
	}
	if req.Start != nil && !*req.Start {
		return result, nil
	}
	startCtx, cancelStart := context.WithTimeout(ctx, startTimeout)
	defer cancelStart()
	if _, err := s.client.post(startCtx, "/containers/"+created.ID+"/start"); err != nil {
		// The container exists: the admin sees it in the list with the error.
		var api *APIError
		if errors.As(err, &api) {
			return result, &conflictError{message: "conteneur créé, mais démarrage refusé : " + api.Error()}
		}
		return result, err
	}
	result.Action = "create-start"
	return result, nil
}

// Handlers.

func decodeBody(w http.ResponseWriter, r *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errBadRequest("requête illisible")
	}
	return nil
}

// resourceFail is fail for images and volumes: the name logged is the one
// Docker resolved, never raw input.
func (s *Service) resourceFail(w http.ResponseWriter, r *http.Request, action, name, what string, err error) {
	status, message := errorStatus(err)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		message = what + " introuvable"
	}
	if errors.Is(err, errImageNotFound) || errors.Is(err, errVolumeNotFound) {
		status = http.StatusNotFound
	}
	if r.Method != http.MethodGet {
		s.audit(r, action, name, "").
			WithFields(logrus.Fields{"status": status, "error": message}).
			Warn("docker resource action refused or failed")
	}
	writeError(w, status, message)
}

func (s *Service) handleImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.Images(r.Context())
	if err != nil {
		s.resourceFail(w, r, "images", "", "image", err)
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (s *Service) handlePull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string `json:"image"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		s.resourceFail(w, r, "pull", "", "image", err)
		return
	}
	result, err := s.Pull(r.Context(), body.Image)
	if err != nil {
		name := ""
		if imageRefPattern.MatchString(body.Image) {
			name = body.Image
		}
		s.resourceFail(w, r, "pull", name, "image", err)
		return
	}
	s.audit(r, "pull", result.Name, "").Info("docker image pulled")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handlePrune(w http.ResponseWriter, r *http.Request) {
	result, err := s.PruneImages(r.Context())
	if err != nil {
		s.resourceFail(w, r, "prune-images", "", "image", err)
		return
	}
	s.audit(r, "prune-images", "", "").WithField("reclaimed", result.Detail).Info("docker images pruned")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	result, err := s.RemoveImage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.resourceFail(w, r, "delete-image", result.Name, "image", err)
		return
	}
	s.audit(r, "delete-image", result.Name, "").Info("docker image deleted")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleVolumes(w http.ResponseWriter, r *http.Request) {
	volumes, err := s.Volumes(r.Context())
	if err != nil {
		s.resourceFail(w, r, "volumes", "", "volume", err)
		return
	}
	writeJSON(w, http.StatusOK, volumes)
}

func (s *Service) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		s.resourceFail(w, r, "create-volume", "", "volume", err)
		return
	}
	result, err := s.CreateVolume(r.Context(), strings.TrimSpace(body.Name))
	if err != nil {
		name := ""
		if volumePattern.MatchString(result.Name) {
			name = result.Name
		}
		s.resourceFail(w, r, "create-volume", name, "volume", err)
		return
	}
	s.audit(r, "create-volume", result.Name, "").Info("docker volume created")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleRemoveVolume(w http.ResponseWriter, r *http.Request) {
	result, err := s.RemoveVolume(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		name := ""
		if volumePattern.MatchString(result.Name) {
			name = result.Name
		}
		s.resourceFail(w, r, "delete-volume", name, "volume", err)
		return
	}
	s.audit(r, "delete-volume", result.Name, "").Info("docker volume deleted")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body CreateRequest
	if err := decodeBody(w, r, &body); err != nil {
		s.resourceFail(w, r, "create", "", "image", err)
		return
	}
	result, err := s.Create(r.Context(), body)
	if err != nil {
		name := ""
		if namePattern.MatchString(result.Name) || idPattern.MatchString(result.Name) {
			name = result.Name
		}
		s.resourceFail(w, r, "create", name, "image", err)
		return
	}
	s.audit(r, result.Action, result.Name, result.ID).Info("docker container created")
	writeJSON(w, http.StatusOK, result)
}
