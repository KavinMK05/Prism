//go:build darwin

package main

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

void markTrayIconAsTemplate() {
    // Walk all status bar items to find ours and mark the image as template.
    // This must run after systray.SetIcon() so the image is already set.
    NSStatusBar *bar = [NSStatusBar systemStatusBar];
    // NSStatusBar doesn't expose -items publicly, so we use a workaround:
    // access the app delegate which systray sets up, and find the status item
    // via the shared NSApplication.
    id delegate = [NSApp delegate];
    if (delegate == nil) return;

    // Use KVC to check if delegate responds to the statusItem accessor pattern
    // systray's AppDelegate stores statusItem as an ivar, not a property,
    // so we use valueForKey: which works on ivars too.
    @try {
        id statusItem = [delegate valueForKey:@"statusItem"];
        if (statusItem != nil && [statusItem isKindOfClass:[NSStatusItem class]]) {
            NSButton *button = [(NSStatusItem *)statusItem button];
            if (button.image != nil) {
                button.image.template = YES;
            }
        }
    } @catch (NSException *e) {
        // Silently fail if the ivar layout changes in a future systray version
    }
}
*/
import "C"

import "github.com/getlantern/systray"

func setPlatformIcon(iconData []byte) {
	systray.SetIcon(iconData)
	C.markTrayIconAsTemplate()
}
