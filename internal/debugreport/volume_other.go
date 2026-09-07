//go:build !windows

package debugreport

// volumeInfo has nothing to say off Windows: there are no drive letters, and a
// mount point that is missing shows up as a plain "does not exist" on the path
// itself. Reported as empty rather than as a guess, so the markdown render can
// omit the column entirely instead of printing "n/a" on every row.
func volumeInfo(string) (volume, kind string) { return "", "" }
