//go:build windows

package platform

import (
	"log"

	"golang.org/x/sys/windows"
)

func AcquireInstanceLock() (func(), error) {
	mutexName, _ := windows.UTF16PtrFromString("PrismSingleInstance")
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		return nil, err
	}

	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		log.Println("Prism is already running")
		windows.CloseHandle(mutex)
		return nil, nil
	}

	return func() {
		windows.CloseHandle(mutex)
	}, nil
}
