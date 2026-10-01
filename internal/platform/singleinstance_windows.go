//go:build windows

package platform

import (
	"fmt"
	"log"

	"golang.org/x/sys/windows"
)

func AcquireInstanceLock() (func(), error) {
	mutexName, _ := windows.UTF16PtrFromString("PrismSingleInstance")
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		return nil, err
	}

	// CreateMutex reports a pre-existing mutex through GetLastError, not through
	// its error return, so it has to be checked explicitly. Returning "already
	// running" as an error (as the darwin and linux implementations do) matters:
	// a second tray process would otherwise start its own admin server and proxy
	// child, and that child's killOrphansOnPort would kill the first instance's
	// proxy. It is also what makes it safe for the installer to start the app
	// while the updater is restarting it.
	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(mutex)
		log.Println("Prism is already running")
		return nil, fmt.Errorf("prism is already running")
	}

	return func() {
		windows.CloseHandle(mutex)
	}, nil
}
