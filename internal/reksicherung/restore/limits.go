// Package restore contains limits shared by restore-provider pipelines.
package restore

// FitsDiskBudget reports whether temporary host files plus the container's
// writable layer fit within maxBytes. maxBytes must be positive.
func FitsDiskBudget(containerBytes, temporaryBytes, maxBytes int64) bool {
	return containerBytes >= 0 &&
		temporaryBytes >= 0 &&
		maxBytes > 0 &&
		containerBytes <= maxBytes &&
		temporaryBytes <= maxBytes-containerBytes
}
