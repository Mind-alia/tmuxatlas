package dockerui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func serveBody(s *Service, method, target, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	s.Register(router, router)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json") // as the interface sends it
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func (f *fakeDocker) postBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func TestImagesListUsageAndOrder(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	response := serve(s, http.MethodGet, "/docker/images", nil)
	var got ImagesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	if len(got.Images) != 3 || got.TotalSize != 260<<20 {
		t.Fatalf("images: %+v", got)
	}
	// Tagged images first, by name; the untagged one last, with no tag at all.
	if got.Images[0].Tags[0] != "app:latest" || got.Images[1].Tags[0] != "nginx:1.27" || len(got.Images[2].Tags) != 0 {
		t.Fatalf("order: %+v", got.Images)
	}
	if len(got.Images[1].Containers) != 1 || got.Images[1].Containers[0] != "web" || len(got.Images[0].Containers) != 0 {
		t.Fatalf("usage: %+v", got.Images)
	}
}

func TestRemoveImage(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	// Several tags: each is untagged, never a forced delete by ID.
	response := serve(s, http.MethodDelete, "/docker/images/"+strings.TrimPrefix(oldImage, "sha256:")[:12], nil)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	calls := f.mutations()
	if len(calls) != 2 || calls[0] != "DELETE /images/app:latest" || calls[1] != "DELETE /images/app:v1" {
		t.Fatalf("Docker received %v", calls)
	}
	for _, call := range calls {
		if strings.Contains(call, "force") {
			t.Fatalf("forced delete: %s", call)
		}
	}
	// In use: Docker's refusal comes back as a conflict.
	response = serve(s, http.MethodDelete, "/docker/images/"+strings.TrimPrefix(nginxImage, "sha256:"), nil)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "being used") {
		t.Fatalf("in use: %d %s", response.Code, response.Body)
	}
	for _, bad := range []string{"nginx:1.27", "zz", "dddddddddddd"} {
		before := len(f.mutations())
		response = serve(s, http.MethodDelete, "/docker/images/"+bad, nil)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", bad, response.Code)
		}
		if len(f.mutations()) != before {
			t.Fatalf("%s reached Docker", bad)
		}
	}
}

func TestPull(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	for _, test := range []struct{ ref, query string }{
		{"nginx", "fromImage=nginx&tag=latest"},
		{"ghcr.io/org/app:1.2", "fromImage=ghcr.io%2Forg%2Fapp&tag=1.2"},
		{"localhost:5000/app", "fromImage=localhost%3A5000%2Fapp&tag=latest"},
	} {
		before := len(f.mutations())
		response := serveBody(s, http.MethodPost, "/docker/images/pull", `{"image":"`+test.ref+`"}`)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Downloaded newer image") {
			t.Fatalf("%s: %d %s", test.ref, response.Code, response.Body)
		}
		if calls := f.mutations(); calls[before] != "POST /images/create?"+test.query {
			t.Fatalf("%s: Docker received %v", test.ref, calls[before:])
		}
	}
	// An error line in the stream is a failure, although Docker answered 200.
	response := serveBody(s, http.MethodPost, "/docker/images/pull", `{"image":"private/app"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "pull access denied") {
		t.Fatalf("refused pull: %d %s", response.Code, response.Body)
	}
	for _, bad := range []string{"", "-x", "a b", "a;rm", "a/../b", "$(x)"} {
		before := len(f.mutations())
		payload, _ := json.Marshal(map[string]string{"image": bad})
		if response := serveBody(s, http.MethodPost, "/docker/images/pull", string(payload)); response.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, response.Code)
		}
		if len(f.mutations()) != before {
			t.Fatalf("%q reached Docker", bad)
		}
	}
}

func TestPruneOnlyDanglingImages(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	response := serveBody(s, http.MethodPost, "/docker/images/prune", "")
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	calls := f.mutations()
	if len(calls) != 1 || !strings.Contains(calls[0], "dangling") {
		t.Fatalf("Docker received %v", calls)
	}
}

func TestVolumes(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	var got VolumesResponse
	// Sizes are read in the background: they come with a later listing.
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := serve(s, http.MethodGet, "/docker/volumes", nil)
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK {
			t.Fatalf("%d %s", response.Code, response.Body)
		}
		if got.Volumes[0].Size != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got.Volumes) != 2 || got.Volumes[0].Name != "shop_data" || got.Volumes[0].Size == nil || *got.Volumes[0].Size != 4096 {
		t.Fatalf("volumes: %+v", got.Volumes)
	}
	if got.Volumes[0].Project != "shop" || len(got.Volumes[0].Containers) != 1 || got.Volumes[0].Containers[0] != "web" {
		t.Fatalf("usage: %+v", got.Volumes[0])
	}
	if got.Volumes[1].Size != nil || len(got.Volumes[1].Containers) != 0 {
		t.Fatalf("unmeasured volume: %+v", got.Volumes[1])
	}

	if response := serveBody(s, http.MethodPost, "/docker/volumes", `{"name":"fresh"}`); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	if bodies := f.postBodies(); bodies[len(bodies)-1] != `{"Name":"fresh"}` {
		t.Fatalf("create body: %v", bodies)
	}
	if response := serveBody(s, http.MethodPost, "/docker/volumes", `{"name":"spare"}`); response.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", response.Code)
	}
	if response := serveBody(s, http.MethodPost, "/docker/volumes", `{"name":"../etc"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", response.Code)
	}

	if response := serve(s, http.MethodDelete, "/docker/volumes/spare", nil); response.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", response.Code, response.Body)
	}
	if response := serve(s, http.MethodDelete, "/docker/volumes/shop_data", nil); response.Code != http.StatusConflict {
		t.Fatalf("in use: %d %s", response.Code, response.Body)
	}
	before := len(f.mutations())
	if response := serve(s, http.MethodDelete, "/docker/volumes/unknown", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", response.Code)
	}
	if calls := f.mutations(); len(calls) != before {
		t.Fatalf("an unlisted volume reached Docker: %v", calls[before:])
	}
	for _, call := range f.mutations() {
		if strings.Contains(call, "force") {
			t.Fatalf("forced delete: %s", call)
		}
	}
}

func TestCreateContainer(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	body := `{"image":"nginx:1.27","name":"new","ports":[{"host":8081,"container":80}],"env":["A=b c"],"volumes":[{"volume":"spare","path":"/data"}],"restart":"unless-stopped"}`
	response := serveBody(s, http.MethodPost, "/docker/containers", body)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	calls := f.mutations()
	if len(calls) != 2 || calls[0] != "POST /containers/create?name=new" || calls[1] != "POST /containers/"+createdID+"/start" {
		t.Fatalf("Docker received %v", calls)
	}
	var sent struct {
		Image      string
		Env        []string
		HostConfig struct {
			PortBindings  map[string][]map[string]string
			Binds         []string
			RestartPolicy map[string]string
			Privileged    bool
		}
	}
	if err := json.Unmarshal([]byte(f.postBodies()[0]), &sent); err != nil {
		t.Fatal(err)
	}
	binding := sent.HostConfig.PortBindings["80/tcp"]
	// Published on the loopback unless the request names another address.
	if len(binding) != 1 || binding[0]["HostIp"] != "127.0.0.1" || binding[0]["HostPort"] != "8081" {
		t.Fatalf("ports: %+v", sent.HostConfig.PortBindings)
	}
	if sent.Image != "nginx:1.27" || len(sent.HostConfig.Binds) != 1 || sent.HostConfig.Binds[0] != "spare:/data" ||
		sent.HostConfig.RestartPolicy["Name"] != "unless-stopped" || sent.HostConfig.Privileged {
		t.Fatalf("body: %s", f.postBodies()[0])
	}

	for name, bad := range map[string]string{
		"host path":   `{"image":"nginx","volumes":[{"volume":"/etc","path":"/host"}]}`,
		"mount root":  `{"image":"nginx","volumes":[{"volume":"spare","path":"/"}]}`,
		"mount dots":  `{"image":"nginx","volumes":[{"volume":"spare","path":"/a/../b"}]}`,
		"port":        `{"image":"nginx","ports":[{"host":70000,"container":80}]}`,
		"host ip":     `{"image":"nginx","ports":[{"host":80,"container":80,"host_ip":"x"}]}`,
		"env":         `{"image":"nginx","env":["novalue"]}`,
		"restart":     `{"image":"nginx","restart":"sometimes"}`,
		"name":        `{"image":"nginx","name":"a b"}`,
		"unknown key": `{"image":"nginx","privileged":true}`,
		"image":       `{"image":""}`,
	} {
		before := len(f.mutations())
		if response := serveBody(s, http.MethodPost, "/docker/containers", bad); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body)
		}
		if len(f.mutations()) != before {
			t.Fatalf("%s reached Docker", name)
		}
	}
	response = serveBody(s, http.MethodPost, "/docker/containers", `{"image":"missing:latest"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "télécharge") {
		t.Fatalf("missing image: %d %s", response.Code, response.Body)
	}
}

// A body sent byte by byte, forever, must not hold the request: the server
// has no ReadTimeout and the body limit bounds the size only. Each route that
// reads a body gives up after its deadline, before anything reaches Docker.
func TestBodiesAreReadWithinADeadline(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	previous := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = previous })
	router := chi.NewRouter()
	s.Register(router, router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	for _, path := range []string{"/docker/containers", "/docker/images/pull", "/docker/volumes"} {
		t.Run(path, func(t *testing.T) {
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			before := len(f.mutations())
			started := time.Now()
			fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: atlas\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{", path)
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				for {
					select {
					case <-stop:
						return
					case <-time.After(50 * time.Millisecond):
						if _, err := conn.Write([]byte(" ")); err != nil {
							return
						}
					}
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			status, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatalf("no answer after %s: %v", time.Since(started).Round(time.Millisecond), err)
			}
			if !strings.HasPrefix(status, "HTTP/1.1 400") || time.Since(started) > 2*time.Second {
				t.Fatalf("answer after %s: %q", time.Since(started).Round(time.Millisecond), status)
			}
			if len(f.mutations()) != before {
				t.Fatalf("Docker reached: %v", f.mutations()[before:])
			}
		})
	}
}
