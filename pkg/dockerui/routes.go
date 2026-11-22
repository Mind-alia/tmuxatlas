package dockerui

import (
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Routes: which domain leads to which container, read from what Docker
// already knows. Nothing is asked to a reverse proxy and no configuration
// file is opened: only the Traefik and caddy-docker-proxy labels of the
// containers, and failing those their published ports.

// Sources of a route.
const (
	routeTraefik = "traefik"
	routeCaddy   = "caddy"
	routePort    = "port"
)

// Route is one way into a container.
type Route struct {
	// Source is traefik, caddy or port (a published port).
	Source string `json:"source"`
	// Host is a domain, or "address:port" on the host for a published port.
	// It is empty when the rule names no domain.
	Host string `json:"host,omitempty"`
	// Path is the path or path prefix of the rule, if any.
	Path string `json:"path,omitempty"`
	// Rule is the label value the route was read from.
	Rule string `json:"rule,omitempty"`
	// Port is the container port; 0 when the labels do not say.
	Port  int    `json:"port,omitempty"`
	Proto string `json:"proto,omitempty"`

	Container   string `json:"container"`
	ContainerID string `json:"container_id"`
	Project     string `json:"compose_project,omitempty"`
	Service     string `json:"compose_service,omitempty"`
	State       string `json:"state"`
}

// ruleTarget is one host and path a Traefik rule matches.
type ruleTarget struct {
	host, path string
}

// matcher is one call of a rule, such as Host(`a`, `b`).
type matcher struct {
	name    string
	args    []string
	negated bool
}

func isQuote(b byte) bool { return b == '`' || b == '"' || b == '\'' }

func isIdent(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// splitAlternatives cuts a rule on the "||" that are outside parentheses and
// quotes.
func splitAlternatives(rule string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(rule); i++ {
		b := rule[i]
		switch {
		case quote != 0:
			if b == quote {
				quote = 0
			}
		case isQuote(b):
			quote = b
		case b == '(':
			depth++
		case b == ')':
			if depth > 0 {
				depth--
			}
		case b == '|' && depth == 0 && i+1 < len(rule) && rule[i+1] == '|':
			out = append(out, rule[start:i])
			start = i + 2
			i++
		}
	}
	return append(out, rule[start:])
}

// scanMatchers finds the matcher calls of an expression, wherever they are
// nested. A call that is not closed is dropped.
func scanMatchers(expr string) []matcher {
	var out []matcher
	var quote byte
	for i := 0; i < len(expr); i++ {
		b := expr[i]
		if quote != 0 {
			if b == quote {
				quote = 0
			}
			continue
		}
		if isQuote(b) {
			quote = b
			continue
		}
		if !isIdent(b) {
			continue
		}
		start := i
		for i < len(expr) && isIdent(expr[i]) {
			i++
		}
		name := expr[start:i]
		if i >= len(expr) || expr[i] != '(' {
			i--
			continue
		}
		args, end, ok := scanArguments(expr, i+1)
		if !ok {
			return out
		}
		negated := strings.HasSuffix(strings.TrimRight(expr[:start], " \t"), "!")
		out = append(out, matcher{name: name, args: args, negated: negated})
		i = end
	}
	return out
}

// scanArguments reads the quoted arguments of a call from the byte after its
// opening parenthesis, and returns the index of the closing one.
func scanArguments(expr string, from int) (args []string, end int, ok bool) {
	for i := from; i < len(expr); i++ {
		b := expr[i]
		switch {
		case b == ')':
			return args, i, true
		case isQuote(b):
			closing := strings.IndexByte(expr[i+1:], b)
			if closing < 0 {
				return nil, 0, false
			}
			args = append(args, expr[i+1:i+1+closing])
			i += closing + 1
		}
	}
	return nil, 0, false
}

// parseTraefikRule lists the hosts and paths a Traefik router rule matches:
// one target per host of each "||" alternative, combined with the path
// matchers of the same alternative. Host and HostRegexp give hosts, Path and
// PathPrefix give paths; negated matchers and the other ones (Headers,
// Method…) are left out. An alternative without host gives a target with an
// empty host. A rule that cannot be read gives nothing.
func parseTraefikRule(rule string) []ruleTarget {
	var out []ruleTarget
	seen := make(map[ruleTarget]bool)
	add := func(target ruleTarget) {
		if !seen[target] {
			seen[target] = true
			out = append(out, target)
		}
	}
	for _, alternative := range splitAlternatives(rule) {
		var hosts, paths []string
		for _, m := range scanMatchers(alternative) {
			if m.negated {
				continue
			}
			var values []string
			for _, arg := range m.args {
				if arg = strings.TrimSpace(arg); arg != "" {
					values = append(values, arg)
				}
			}
			switch strings.ToLower(m.name) {
			case "host", "hostregexp":
				hosts = append(hosts, values...)
			case "path", "pathprefix":
				paths = append(paths, values...)
			}
		}
		if len(hosts) == 0 && len(paths) == 0 {
			continue
		}
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		if len(paths) == 0 {
			paths = []string{""}
		}
		for _, host := range hosts {
			for _, path := range paths {
				add(ruleTarget{host: host, path: path})
			}
		}
	}
	return out
}

// labelPort reads a port number from a label value.
func labelPort(value string) int {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

// traefikRoutes reads the HTTP routers of a container's labels. Label names
// are matched without case, as Traefik does. traefik.enable=false hides the
// container from Traefik, so it gives no route.
func traefikRoutes(labels map[string]string) []Route {
	const routers, services = "traefik.http.routers.", "traefik.http.services."
	const portSuffix = ".loadbalancer.server.port"
	rules := make(map[string]string)
	routerService := make(map[string]string)
	ports := make(map[string]int)
	for key, value := range labels {
		lower := strings.ToLower(key)
		switch {
		case lower == "traefik.enable":
			if strings.EqualFold(strings.TrimSpace(value), "false") {
				return nil
			}
		case strings.HasPrefix(lower, routers):
			name, field, _ := strings.Cut(lower[len(routers):], ".")
			switch field {
			case "rule":
				rules[name] = value
			case "service":
				routerService[name] = strings.ToLower(strings.TrimSpace(value))
			}
		case strings.HasPrefix(lower, services) && strings.HasSuffix(lower, portSuffix):
			name := lower[len(services) : len(lower)-len(portSuffix)]
			if port := labelPort(value); port != 0 && name != "" {
				ports[name] = port
			}
		}
	}
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Route
	for _, name := range names {
		rule := strings.TrimSpace(rules[name])
		if rule == "" {
			continue
		}
		port := 0
		if service, set := routerService[name]; set {
			// "api@docker" is the service "api" of these labels; a service of
			// another provider ("api@file") is not described here.
			port = ports[strings.TrimSuffix(service, "@docker")]
		} else if own, ok := ports[name]; ok {
			port = own
		} else if len(ports) == 1 {
			for _, only := range ports {
				port = only
			}
		}
		targets := parseTraefikRule(rule)
		if len(targets) == 0 {
			targets = []ruleTarget{{}} // unreadable: the rule itself is shown
		}
		for _, target := range targets {
			out = append(out, Route{Source: routeTraefik, Host: target.host, Path: target.path, Rule: rule, Port: port})
		}
	}
	return out
}

var (
	// caddySite is a site label of caddy-docker-proxy: "caddy" or "caddy_3".
	caddySite = regexp.MustCompile(`^caddy(_\d+)?$`)
	// caddyUpstreams is its "{{upstreams 8080}}" or "{{upstreams https 8443}}".
	caddyUpstreams = regexp.MustCompile(`\{\{\s*upstreams(?:\s+[a-z][a-z0-9]*)?(?:\s+(\d+))?\s*\}\}`)
	caddyNumbered  = regexp.MustCompile(`_\d+$`)
)

// caddyRoutes reads the simple form of caddy-docker-proxy labels: a site
// label ("caddy: example.com") and, under it, a reverse_proxy to this
// container ("caddy.reverse_proxy: {{upstreams 8080}}"). Snippets, imports
// and nested handlers are not interpreted.
func caddyRoutes(labels map[string]string) []Route {
	var sites []string
	for key := range labels {
		if caddySite.MatchString(key) {
			sites = append(sites, key)
		}
	}
	sort.Strings(sites)
	var out []Route
	for _, site := range sites {
		value := strings.TrimSpace(labels[site])
		if value == "" || strings.HasPrefix(value, "(") { // a snippet, not a site
			continue
		}
		port, path := 0, ""
		var proxies []string
		for key := range labels {
			if rest, ok := strings.CutPrefix(key, site+"."); ok && caddyNumbered.ReplaceAllString(rest, "") == "reverse_proxy" {
				proxies = append(proxies, key)
			}
		}
		sort.Strings(proxies)
		if len(proxies) > 0 {
			proxy := strings.TrimSpace(labels[proxies[0]])
			if match := caddyUpstreams.FindStringSubmatch(proxy); match != nil {
				port = labelPort(match[1])
			}
			if first, _, _ := strings.Cut(proxy, " "); strings.HasPrefix(first, "/") {
				path = first
			}
		}
		for _, address := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			host := address
			if _, rest, found := strings.Cut(address, "://"); found {
				host = rest
			}
			if host == "" {
				continue
			}
			out = append(out, Route{Source: routeCaddy, Host: host, Path: path, Rule: value, Port: port})
		}
	}
	return out
}

// labelRoutes returns the routes written in a container's labels.
func labelRoutes(labels map[string]string) []Route {
	return append(traefikRoutes(labels), caddyRoutes(labels)...)
}

// portRoutes presents the published ports as routes: "address:port" on the
// host leads to the container port.
func portRoutes(ports []Port) []Route {
	var out []Route
	for _, p := range ports {
		if p.Public == 0 {
			continue
		}
		ip := p.IP
		if ip == "" || ip == "::" {
			ip = "0.0.0.0"
		}
		out = append(out, Route{Source: routePort, Host: net.JoinHostPort(ip, strconv.Itoa(p.Public)), Port: p.Private, Proto: p.Type})
	}
	return out
}

// containerRoutes returns the routes of one container: those of its labels
// or, when the labels give none, its published ports.
func containerRoutes(c Container, labels map[string]string) []Route {
	routes := labelRoutes(labels)
	if len(routes) == 0 {
		routes = portRoutes(c.Ports)
	}
	for i := range routes {
		routes[i].Container = c.Name
		routes[i].ContainerID = c.ID
		routes[i].Project = c.Project
		routes[i].Service = c.Service
		routes[i].State = c.State
	}
	return routes
}

// sortRoutes puts proxy routes first, then published ports, each by host.
func sortRoutes(routes []Route) {
	rank := map[string]int{routeTraefik: 0, routeCaddy: 1, routePort: 2}
	sort.SliceStable(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if rank[a.Source] != rank[b.Source] {
			return rank[a.Source] < rank[b.Source]
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Container < b.Container
	})
}
