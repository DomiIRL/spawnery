/*
Copyright paul_wtf.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package netstatus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func metricsFrom(t *testing.T, status int, body string) APIMetrics {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/metrics.k8s.io/v1beta1/namespaces/games/pods" {
			t.Errorf("asked %s, want the games namespace's pod metrics", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return APIMetrics{REST: cs.Discovery().RESTClient()}
}

func TestPodUsageSumsContainers(t *testing.T) {
	m := metricsFrom(t, http.StatusOK, `{"items":[
	  {"metadata":{"name":"lobby-a"},"containers":[
	    {"name":"server","usage":{"cpu":"250m","memory":"1Gi"}},
	    {"name":"sidecar","usage":{"cpu":"1500000n","memory":"64Mi"}}]},
	  {"metadata":{"name":"gateway-b"},"containers":[
	    {"name":"proxy","usage":{"cpu":"1","memory":"512Mi"}}]}]}`)

	got, err := m.PodUsage(context.Background(), "games")
	if err != nil {
		t.Fatal(err)
	}
	// 250m + 1.5m, summed as quantities and rounded up once.
	if u := got["lobby-a"]; u.CPUMilli != 252 || u.MemoryBytes != (1<<30)+(64<<20) {
		t.Errorf("lobby-a = %+v, want 252m and 1Gi+64Mi", u)
	}
	if u := got["gateway-b"]; u.CPUMilli != 1000 || u.MemoryBytes != 512<<20 {
		t.Errorf("gateway-b = %+v, want 1000m and 512Mi", u)
	}
}

func TestPodUsageFailsWithoutTheAPI(t *testing.T) {
	m := metricsFrom(t, http.StatusNotFound, `{"kind":"Status","code":404}`)
	if _, err := m.PodUsage(context.Background(), "games"); err == nil {
		t.Fatal("a missing metrics API read as an empty network")
	}
}
