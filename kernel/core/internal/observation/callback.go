package observation

import "fmt"

// Call isolates passive callback panics. Its diagnostic is local and cannot
// veto a commit, settlement or resource release. Control hooks do not use it.
func Call(name string, callback func()) (err error) {
	completed := false
	defer func() {
		cause := recover()
		if completed {
			return
		}
		if original, ok := cause.(error); ok {
			err = fmt.Errorf("observation %s panicked: %w", name, original)
		} else {
			err = fmt.Errorf("observation %s panicked: %v", name, cause)
		}
	}()
	callback()
	completed = true
	return nil
}
