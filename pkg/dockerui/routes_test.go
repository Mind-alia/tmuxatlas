package dockerui

import (
	"reflect"
	"testing"
)

func TestParseTraefikRule(t *testing.T) {
	for _, test := range []struct {
		rule string
		want []ruleTarget
	}{
		{"Host(`a.example.com`)", []ruleTarget{{host: "a.example.com"}}},
		{"Host(`a.example.com`) || Host(`b.example.com`)", []ruleTarget{{host: "a.example.com"}, {host: "b.example.com"}}},
		{"Host(`a.example.com`, `b.example.com`)", []ruleTarget{{host: "a.example.com"}, {host: "b.example.com"}}},
		{"Host(`a.example.com`,`b.example.com`)||Host(`c.example.com`)", []ruleTarget{{host: "a.example.com"}, {host: "b.example.com"}, {host: "c.example.com"}}},
		{`Host("a.example.com") && PathPrefix("/api")`, []ruleTarget{{host: "a.example.com", path: "/api"}}},
		{"Host(`a.example.com`) && PathPrefix(`/api`) || Host(`b.example.com`)", []ruleTarget{{host: "a.example.com", path: "/api"}, {host: "b.example.com"}}},
		{"(Host(`a.example.com`) || Host(`b.example.com`)) && PathPrefix(`/x`)", []ruleTarget{{host: "a.example.com", path: "/x"}, {host: "b.example.com", path: "/x"}}},
		{"Host(`a.example.com`) && (PathPrefix(`/x`) || Path(`/y`))", []ruleTarget{{host: "a.example.com", path: "/x"}, {host: "a.example.com", path: "/y"}}},
		{"PathPrefix(`/static`)", []ruleTarget{{path: "/static"}}},
		{"HostRegexp(`^(a|b)\\.example\\.com$`)", []ruleTarget{{host: `^(a|b)\.example\.com$`}}},
		{"Host(`a.example.com`) && !PathPrefix(`/admin`)", []ruleTarget{{host: "a.example.com"}}},
		{"Host(`a.example.com`) && Headers(`X-Env`, `prod`) && Method(`GET`)", []ruleTarget{{host: "a.example.com"}}},
		{"  host( `a.example.com` )  ", []ruleTarget{{host: "a.example.com"}}},
		{"Host(`a.example.com`) || Host(`a.example.com`)", []ruleTarget{{host: "a.example.com"}}},
		{"Host(`a || b.example.com`)", []ruleTarget{{host: "a || b.example.com"}}},
		// Absent or malformed rules give nothing rather than a guess.
		{"", nil},
		{"Host", nil},
		{"Host()", nil},
		{"Host(``)", nil},
		{"Host(`a.example.com`", nil},
		{"Host(`a.example.com)", nil},
		{"Host(a.example.com)", nil},
		{"Headers(`X-Env`, `prod`)", nil},
		{"||", nil},
		{"))((`", nil},
		{"Host(`a.example.com`) || Host(`b.example.com", []ruleTarget{{host: "a.example.com"}}},
	} {
		if got := parseTraefikRule(test.rule); !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseTraefikRule(%q) = %+v, want %+v", test.rule, got, test.want)
		}
	}
}

func TestTraefikRoutes(t *testing.T) {
	for _, test := range []struct {
		name   string
		labels map[string]string
		want   []Route
	}{
		{"no label", nil, nil},
		{"labels of something else", map[string]string{"com.example.team": "web", "traefik.docker.network": "edge"}, nil},
		{"one router and its service", map[string]string{
			"traefik.enable":                                      "true",
			"traefik.http.routers.site.rule":                      "Host(`www.example.com`)",
			"traefik.http.routers.site.entrypoints":               "websecure",
			"traefik.http.routers.site.tls.certresolver":          "le",
			"traefik.http.services.site.loadbalancer.server.port": "8080",
		}, []Route{{Source: routeTraefik, Host: "www.example.com", Rule: "Host(`www.example.com`)", Port: 8080}}},
		{"disabled", map[string]string{
			"traefik.enable":                 "false",
			"traefik.http.routers.site.rule": "Host(`www.example.com`)",
		}, nil},
		{"enable is read without case", map[string]string{
			"Traefik.Enable":                 " False ",
			"traefik.http.routers.site.rule": "Host(`www.example.com`)",
		}, nil},
		{"the only service serves a router with another name", map[string]string{
			"traefik.http.routers.front.rule":                     "Host(`a.example.com`, `b.example.com`)",
			"traefik.http.services.back.loadbalancer.server.port": "3000",
		}, []Route{
			{Source: routeTraefik, Host: "a.example.com", Rule: "Host(`a.example.com`, `b.example.com`)", Port: 3000},
			{Source: routeTraefik, Host: "b.example.com", Rule: "Host(`a.example.com`, `b.example.com`)", Port: 3000},
		}},
		{"routers name their service", map[string]string{
			"traefik.http.routers.api.rule":                            "Host(`api.example.com`) && PathPrefix(`/v1`)",
			"traefik.http.routers.api.service":                         "api-svc@docker",
			"traefik.http.routers.admin.rule":                          "Host(`admin.example.com`)",
			"traefik.http.routers.admin.service":                       "admin-svc",
			"traefik.http.routers.ext.rule":                            "Host(`ext.example.com`)",
			"traefik.http.routers.ext.service":                         "elsewhere@file",
			"traefik.http.services.api-svc.loadbalancer.server.port":   "8000",
			"traefik.http.services.admin-svc.loadbalancer.server.port": "9000",
		}, []Route{
			{Source: routeTraefik, Host: "admin.example.com", Rule: "Host(`admin.example.com`)", Port: 9000},
			{Source: routeTraefik, Host: "api.example.com", Path: "/v1", Rule: "Host(`api.example.com`) && PathPrefix(`/v1`)", Port: 8000},
			{Source: routeTraefik, Host: "ext.example.com", Rule: "Host(`ext.example.com`)"},
		}},
		{"two services and no link: the port is unknown", map[string]string{
			"traefik.http.routers.site.rule":                   "Host(`www.example.com`)",
			"traefik.http.services.a.loadbalancer.server.port": "1000",
			"traefik.http.services.b.loadbalancer.server.port": "2000",
		}, []Route{{Source: routeTraefik, Host: "www.example.com", Rule: "Host(`www.example.com`)"}}},
		{"no port label", map[string]string{"traefik.http.routers.site.rule": "Host(`www.example.com`)"},
			[]Route{{Source: routeTraefik, Host: "www.example.com", Rule: "Host(`www.example.com`)"}}},
		{"malformed port", map[string]string{
			"traefik.http.routers.site.rule":                      "Host(`www.example.com`)",
			"traefik.http.services.site.loadbalancer.server.port": "http",
		}, []Route{{Source: routeTraefik, Host: "www.example.com", Rule: "Host(`www.example.com`)"}}},
		{"port out of range", map[string]string{
			"traefik.http.routers.site.rule":                      "Host(`www.example.com`)",
			"traefik.http.services.site.loadbalancer.server.port": "70000",
		}, []Route{{Source: routeTraefik, Host: "www.example.com", Rule: "Host(`www.example.com`)"}}},
		{"a rule that cannot be read is shown as it is", map[string]string{"traefik.http.routers.site.rule": "Host(`www.example.com"},
			[]Route{{Source: routeTraefik, Rule: "Host(`www.example.com"}}},
		{"empty rule", map[string]string{"traefik.http.routers.site.rule": "  "}, nil},
		{"router without rule", map[string]string{
			"traefik.http.routers.site.entrypoints":               "web",
			"traefik.http.services.site.loadbalancer.server.port": "80",
		}, nil},
		{"truncated label names", map[string]string{"traefik.http.routers.": "x", "traefik.http.routers.site": "y", "traefik.http.services..loadbalancer.server.port": "80"}, nil},
		{"TCP routers are not HTTP routes", map[string]string{"traefik.tcp.routers.db.rule": "HostSNI(`db.example.com`)"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := traefikRoutes(test.labels); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got  %+v\nwant %+v", got, test.want)
			}
		})
	}
}

func TestCaddyRoutes(t *testing.T) {
	for _, test := range []struct {
		name   string
		labels map[string]string
		want   []Route
	}{
		{"no label", map[string]string{"caddyfile": "x", "caddy.reverse_proxy": "{{upstreams 80}}"}, nil},
		{"site and upstream port", map[string]string{"caddy": "www.example.com", "caddy.reverse_proxy": "{{upstreams 8080}}"},
			[]Route{{Source: routeCaddy, Host: "www.example.com", Rule: "www.example.com", Port: 8080}}},
		{"several addresses, schemes dropped", map[string]string{"caddy": "https://a.example.com, b.example.com  http://c.example.com:8080", "caddy.reverse_proxy": "{{ upstreams https 8443 }}"},
			[]Route{
				{Source: routeCaddy, Host: "a.example.com", Rule: "https://a.example.com, b.example.com  http://c.example.com:8080", Port: 8443},
				{Source: routeCaddy, Host: "b.example.com", Rule: "https://a.example.com, b.example.com  http://c.example.com:8080", Port: 8443},
				{Source: routeCaddy, Host: "c.example.com:8080", Rule: "https://a.example.com, b.example.com  http://c.example.com:8080", Port: 8443},
			}},
		{"numbered sites and a path matcher", map[string]string{
			"caddy_0": "a.example.com", "caddy_0.reverse_proxy": "/api/* {{upstreams 3000}}",
			"caddy_1": "b.example.com", "caddy_1.reverse_proxy_0": "{{upstreams}}",
		}, []Route{
			{Source: routeCaddy, Host: "a.example.com", Path: "/api/*", Rule: "a.example.com", Port: 3000},
			{Source: routeCaddy, Host: "b.example.com", Rule: "b.example.com"},
		}},
		{"site without reverse_proxy", map[string]string{"caddy": "static.example.com", "caddy.file_server": ""},
			[]Route{{Source: routeCaddy, Host: "static.example.com", Rule: "static.example.com"}}},
		{"snippets and empty sites are skipped", map[string]string{"caddy_0": "(common)", "caddy_0.encode": "gzip", "caddy_1": "  "}, nil},
		{"malformed upstream", map[string]string{"caddy": "www.example.com", "caddy.reverse_proxy": "{{upstreams abc def}} {{upstreams 99999}}"},
			[]Route{{Source: routeCaddy, Host: "www.example.com", Rule: "www.example.com"}}},
		{"nested handlers are not interpreted", map[string]string{"caddy": "www.example.com", "caddy.handle_path.reverse_proxy": "{{upstreams 8080}}"},
			[]Route{{Source: routeCaddy, Host: "www.example.com", Rule: "www.example.com"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := caddyRoutes(test.labels); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got  %+v\nwant %+v", got, test.want)
			}
		})
	}
}

func TestContainerRoutesFallBackToPublishedPorts(t *testing.T) {
	c := Container{ID: webID, Name: "shop-web-1", Project: "shop", Service: "web", State: "running", Ports: []Port{
		{IP: "127.0.0.1", Private: 80, Public: 8080, Type: "tcp"},
		{IP: "::", Private: 53, Public: 5353, Type: "udp"},
		{IP: "::1", Private: 81, Public: 8081, Type: "tcp"},
		{Private: 443, Type: "tcp"},
	}}
	got := containerRoutes(c, map[string]string{"com.example.team": "web"})
	base := Route{Source: routePort, Container: "shop-web-1", ContainerID: webID, Project: "shop", Service: "web", State: "running"}
	want := []Route{base, base, base}
	want[0].Host, want[0].Port, want[0].Proto = "127.0.0.1:8080", 80, "tcp"
	want[1].Host, want[1].Port, want[1].Proto = "0.0.0.0:5353", 53, "udp"
	want[2].Host, want[2].Port, want[2].Proto = "[::1]:8081", 81, "tcp"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published ports:\ngot  %+v\nwant %+v", got, want)
	}

	// Labels win: the published ports are then not repeated.
	got = containerRoutes(c, map[string]string{
		"traefik.http.routers.shop.rule":                      "Host(`shop.example.com`)",
		"traefik.http.services.shop.loadbalancer.server.port": "80",
		"caddy": "boutique.example.com", "caddy.reverse_proxy": "{{upstreams 80}}",
	})
	if len(got) != 2 || got[0].Source != routeTraefik || got[0].Host != "shop.example.com" || got[0].Port != 80 ||
		got[1].Source != routeCaddy || got[1].Host != "boutique.example.com" || got[0].Container != "shop-web-1" || got[1].State != "running" {
		t.Fatalf("label routes: %+v", got)
	}
	if got := containerRoutes(Container{Name: "idle", Ports: []Port{{Private: 80, Type: "tcp"}}}, nil); len(got) != 0 {
		t.Fatalf("a container without label nor published port has no route: %+v", got)
	}

	routes := []Route{
		{Source: routePort, Host: "0.0.0.0:80", Container: "b"}, {Source: routeCaddy, Host: "z.example.com"},
		{Source: routeTraefik, Host: "b.example.com", Path: "/b"}, {Source: routeTraefik, Host: "b.example.com", Path: "/a"},
		{Source: routePort, Host: "0.0.0.0:80", Container: "a"}, {Source: routeTraefik, Host: "a.example.com"},
	}
	sortRoutes(routes)
	var order []string
	for _, r := range routes {
		order = append(order, r.Source+" "+r.Host+r.Path+" "+r.Container)
	}
	wantOrder := []string{"traefik a.example.com ", "traefik b.example.com/a ", "traefik b.example.com/b ", "caddy z.example.com ", "port 0.0.0.0:80 a", "port 0.0.0.0:80 b"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order: %v", order)
	}
}
