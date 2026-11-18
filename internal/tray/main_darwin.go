//go:build darwin && cgo

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#import <Cocoa/Cocoa.h>

static NSStatusItem *item;

@interface TrayTarget : NSObject
- (void)open:(NSMenuItem *)sender;
@end

@implementation TrayTarget
- (void)open:(NSMenuItem *)sender {
	[[NSWorkspace sharedWorkspace] openURL:[NSURL fileURLWithPath:sender.representedObject]];
}
@end

static TrayTarget *target;

static void trayRun(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	target = [TrayTarget new];
	[NSApp run];
}

static NSImage *dot(CGFloat r, CGFloat g, CGFloat b) {
	return [NSImage imageWithSize:NSMakeSize(16, 16) flipped:NO drawingHandler:^BOOL(NSRect rect) {
		[[NSColor colorWithSRGBRed:r green:g blue:b alpha:1] setFill];
		[[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(3, 3, 10, 10)] fill];
		return YES;
	}];
}

static void trayUpdate(int visible, double r, double g, double b, const char *title, const char *rows) {
	NSString *t = [NSString stringWithUTF8String:title];
	NSString *rs = [NSString stringWithUTF8String:rows];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (!visible) {
			if (item != nil) {
				[[NSStatusBar systemStatusBar] removeStatusItem:item];
				item = nil;
			}
			return;
		}
		if (item == nil) {
			item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
		}
		item.button.image = dot(r, g, b);
		item.button.title = t;
		item.button.imagePosition = NSImageLeft;
		NSMenu *menu = [NSMenu new];
		for (NSString *line in [rs componentsSeparatedByString:@"\n"]) {
			NSArray *f = [line componentsSeparatedByString:@"\t"];
			if (f.count != 2) {
				continue;
			}
			NSMenuItem *mi = [[NSMenuItem alloc] initWithTitle:f[1] action:@selector(open:) keyEquivalent:@""];
			mi.target = target;
			mi.representedObject = f[0];
			[menu addItem:mi];
		}
		item.menu = menu;
	});
}
*/
import "C"

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

func init() {
	runtime.LockOSThread()
}

func Main(run func() int) {
	mainLoop = true
	done := make(chan int, 1)
	go func() { done <- run() }()
	select {
	case code := <-done:
		os.Exit(code)
	case dir := <-startCh:
		go func() { os.Exit(<-done) }()
		go watch(dir, show)
		C.trayRun()
	}
}

var colours = map[State][3]float64{
	Running: {0x6E / 255.0, 0x9F / 255.0, 0xC4 / 255.0},
	Waiting: {0xE0 / 255.0, 0xA4 / 255.0, 0x58 / 255.0},
	Paused:  {0x8A / 255.0, 0x92 / 255.0, 0x9E / 255.0},
}

func show(v View) {
	title := ""
	if len(v.Rows) > 1 {
		title = " " + strconv.Itoa(len(v.Rows))
	}
	var rows []string
	for _, r := range v.Rows {
		rows = append(rows, r.Repo+"\t"+r.Label)
	}
	ct, cr := C.CString(title), C.CString(strings.Join(rows, "\n"))
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cr))
	c := colours[v.State]
	vis := C.int(0)
	if v.Visible {
		vis = 1
	}
	C.trayUpdate(vis, C.double(c[0]), C.double(c[1]), C.double(c[2]), ct, cr)
}
