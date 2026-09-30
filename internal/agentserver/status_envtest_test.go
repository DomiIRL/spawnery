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

package agentserver_test

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	spawneryv1alpha1 "github.com/spawnery/spawnery/api/v1alpha1"
	"github.com/spawnery/spawnery/internal/agentpb"
	"github.com/spawnery/spawnery/internal/netstatus"
	"github.com/spawnery/spawnery/internal/podspec"
)

// fixtureMetrics stands in for metrics-server, which envtest does not run.
type fixtureMetrics struct{}

func (fixtureMetrics) PodUsage(context.Context, string) (map[string]netstatus.Usage, error) {
	return map[string]netstatus.Usage{"lobby-aaaa": {CPUMilli: 100, MemoryBytes: 256 << 20}}, nil
}

func TestStatusOverTheWireCarriesReportedTicks(t *testing.T) {
	f := newServerFixture(t)
	pod := f.pod("lobby-aaaa")
	makeServer(t, f, "lobby-aaaa")
	var srv spawneryv1alpha1.Server
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: f.ns, Name: "lobby-aaaa"}, &srv); err != nil {
		t.Fatal(err)
	}
	srv.Status.PodName, srv.Status.PodUID = pod.Name, string(pod.UID)
	if err := f.c.Status().Update(f.ctx, &srv); err != nil {
		t.Fatal(err)
	}

	stream, done := dialAgent(t, f.ctx, f.addr, f.ca,
		f.token(podspec.ServerServiceAccountName, []string{podspec.AgentTokenAudience}, pod))
	defer done()
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("the opening message never arrived: %v", err)
	}
	ask := func(id uint64, target string) *agentpb.CloudResponse {
		t.Helper()
		if err := stream.Send(&agentpb.ServerMessage{Message: &agentpb.ServerMessage_CloudRequest{
			CloudRequest: &agentpb.CloudRequest{Id: id,
				Request: &agentpb.CloudRequest_Status{Status: &agentpb.StatusRequest{Target: target}}},
		}}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if resp := msg.GetCloudResponse(); resp != nil && resp.GetId() == id {
				return resp
			}
			if time.Now().After(deadline) {
				t.Fatal("no answer within ten seconds")
			}
		}
	}

	if err := stream.Send(&agentpb.ServerMessage{Message: &agentpb.ServerMessage_PlayerCount{
		PlayerCount: &agentpb.PlayerCount{Players: 1, Slots: 20, Tps: 18.5, Mspt: 31,
			HeapUsedBytes: 1 << 30, HeapMaxBytes: 4 << 30},
	}}); err != nil {
		t.Fatal(err)
	}
	resp := ask(21, "lobby-aaaa")
	if snap := f.agents.Lookup(string(pod.UID)); snap.HeapUsed != 1<<30 || snap.HeapMax != 4<<30 {
		t.Errorf("heap = %v/%v in the registry, want 1GiB/4GiB", snap.HeapUsed, snap.HeapMax)
	}
	in := resp.GetStatus().GetInstances()
	if len(in) != 1 || in[0].GetTps() != 18.5 || in[0].GetMspt() != 31 {
		t.Fatalf("answer = %+v, want lobby-aaaa at 18.5 TPS and 31 ms", resp)
	}
	if !resp.GetStatus().GetMetricsAvailable() || in[0].GetUsage().GetCpuUsedMillicores() != 100 {
		t.Errorf("usage = %+v, want the fixture's 100m", in[0].GetUsage())
	}
	if unknown := ask(22, "nowhere"); unknown.GetError().GetReason() != agentpb.RequestError_NOT_FOUND {
		t.Errorf("unknown target -> %+v, want NOT_FOUND", unknown)
	}
}
