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
package cloud.spawnery.agent.api;

import java.time.Duration;
import java.util.Objects;
import java.util.OptionalDouble;

/**
 * One server or proxy in a {@link NetworkStatus}. {@code phase} is empty for
 * a proxy, whose state is {@code ready} and {@code draining}. {@code tps} and
 * {@code mspt} are empty for a proxy and for a server that has not reported.
 * {@code node} is the Kubernetes node the pod runs on, empty while it is not
 * scheduled. {@code playableSlots} is how many of {@code slots} count as
 * capacity, equal to {@code slots} when nothing narrowed it.
 */
public record InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready,
                             int players, int slots, OptionalDouble tps, OptionalDouble mspt, Duration age,
                             boolean retiring, boolean held, boolean draining, ResourceUsage usage,
                             String node, int playableSlots) {
    public InstanceStatus {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(group, "group");
        Objects.requireNonNull(phase, "phase");
        Objects.requireNonNull(tps, "tps");
        Objects.requireNonNull(mspt, "mspt");
        Objects.requireNonNull(age, "age");
        Objects.requireNonNull(usage, "usage");
        node = node == null ? "" : node;
        if (playableSlots <= 0 || playableSlots > slots) {
            playableSlots = slots;
        }
    }

    /** The record as it was before {@code playableSlots}, which it reads as every seat. */
    public InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready,
                          int players, int slots, OptionalDouble tps, OptionalDouble mspt, Duration age,
                          boolean retiring, boolean held, boolean draining, ResourceUsage usage,
                          String node) {
        this(name, group, proxy, phase, ready, players, slots, tps, mspt, age, retiring, held, draining, usage, node, slots);
    }

    /** The record as it was before {@code node}, which it reads as not scheduled. */
    public InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready,
                          int players, int slots, OptionalDouble tps, OptionalDouble mspt, Duration age,
                          boolean retiring, boolean held, boolean draining, ResourceUsage usage) {
        this(name, group, proxy, phase, ready, players, slots, tps, mspt, age, retiring, held, draining, usage, "", slots);
    }
}
