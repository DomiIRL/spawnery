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

/**
 * What a set of pods uses right now, and what they asked for. CPU in
 * millicores, memory in bytes. The used figures cover {@code podsMeasured}
 * of {@code pods}; a pod the metrics API has no sample for is left out
 * rather than counted as idle.
 *
 * <p>{@code cpuUnlimited} (and {@code memoryUnlimited}) means some container
 * has no limit, so the limit figure is a floor.
 */
public record ResourceUsage(
        long cpuUsedMillicores, long cpuRequestedMillicores, long cpuLimitMillicores, boolean cpuUnlimited,
        long memoryUsedBytes, long memoryRequestedBytes, long memoryLimitBytes, boolean memoryUnlimited,
        int pods, int podsMeasured) {

    /** Whether any pod in the set was measured. */
    public boolean measured() {
        return podsMeasured > 0;
    }

    /** Whether every pod in the set was measured. */
    public boolean complete() {
        return podsMeasured == pods;
    }
}
