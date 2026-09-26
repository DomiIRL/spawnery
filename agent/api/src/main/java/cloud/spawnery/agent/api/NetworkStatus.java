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

import java.util.List;
import java.util.Objects;

/**
 * The answer to {@link SpawneryApi#status()}: this network's usage and tick
 * rates, never anything outside its namespace.
 *
 * <p>For the whole network, {@code groups} has every group and
 * {@code instances} is empty; {@code other} is the namespace's pods outside
 * every listed group. For a group, {@code groups} is that group and
 * {@code instances} its members. For one server or proxy, {@code groups} is
 * empty and {@code instances} is that one. {@code total} is always the usage
 * of what was asked about.
 *
 * <p>{@code metricsAvailable} false means the cluster serves no metrics API;
 * every usage then has requests and limits but nothing measured.
 */
public record NetworkStatus(ResourceUsage total, List<GroupStatus> groups, List<InstanceStatus> instances,
                            ResourceUsage other, boolean metricsAvailable, int players, int servers, int proxies) {
    public NetworkStatus {
        Objects.requireNonNull(total, "total");
        groups = List.copyOf(groups);
        instances = List.copyOf(instances);
        Objects.requireNonNull(other, "other");
    }
}
