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

import java.util.Objects;
import java.util.OptionalDouble;

/**
 * One group in a {@link NetworkStatus}. {@code lowestTps} is empty for a proxy
 * group and for a group none of whose servers reported one.
 */
public record GroupStatus(String name, Group.Kind kind, String phase, int replicas, int readyReplicas, int players,
                          OptionalDouble lowestTps, ResourceUsage usage) {
    public GroupStatus {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(kind, "kind");
        Objects.requireNonNull(phase, "phase");
        Objects.requireNonNull(lowestTps, "lowestTps");
        Objects.requireNonNull(usage, "usage");
    }
}
