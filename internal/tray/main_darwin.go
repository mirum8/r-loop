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

static NSImage *ring(int state, double progress) {
	BOOL waiting = state == 1;
	NSImage *img = [NSImage imageWithSize:NSMakeSize(18, 18) flipped:NO drawingHandler:^BOOL(NSRect rect) {
		NSColor *ink = waiting ? [NSColor labelColor] : [NSColor blackColor];
		NSColor *fill = waiting ? [NSColor colorWithSRGBRed:0xE0/255.0 green:0xA4/255.0 blue:0x58/255.0 alpha:1] : ink;
		NSBezierPath *track = [NSBezierPath bezierPathWithOvalInRect:NSMakeRect(3, 3, 12, 12)];
		track.lineWidth = 2.2;
		[[ink colorWithAlphaComponent:0.3] setStroke];
		[track stroke];
		if (progress > 0) {
			NSBezierPath *arc = [NSBezierPath bezierPath];
			[arc appendBezierPathWithArcWithCenter:NSMakePoint(9, 9) radius:6 startAngle:90 endAngle:90 - 360 * fmin(progress, 1) clockwise:YES];
			arc.lineWidth = 2.2;
			arc.lineCapStyle = NSLineCapStyleRound;
			[fill setStroke];
			[arc stroke];
		}
		if (state == 2) {
			[ink setFill];
			NSRectFill(NSMakeRect(7, 6.5, 1.5, 5));
			NSRectFill(NSMakeRect(9.5, 6.5, 1.5, 5));
		}
		return YES;
	}];
	img.template = !waiting;
	return img;
}

static void trayUpdate(int visible, int state, double progress, const char *title, const char *rows) {
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
		item.button.image = ring(state, progress);
		item.button.imagePosition = NSImageLeft;
		NSMutableDictionary *attrs = [NSMutableDictionary dictionaryWithObject:[NSFont menuBarFontOfSize:0] forKey:NSFontAttributeName];
		if (state == 1) {
			attrs[NSForegroundColorAttributeName] = [NSColor colorWithSRGBRed:0xE0/255.0 green:0xA4/255.0 blue:0x58/255.0 alpha:1];
		}
		item.button.attributedTitle = [[NSAttributedString alloc] initWithString:t attributes:attrs];
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

var codes = map[State]C.int{Running: 0, Waiting: 1, Paused: 2}

func show(v View) {
	title, progress := "", -1.0
	if v.Total > 0 {
		title = " " + strconv.Itoa(v.Done) + "/" + strconv.Itoa(v.Total)
		progress = float64(v.Done) / float64(v.Total)
	}
	var rows []string
	for _, r := range v.Rows {
		rows = append(rows, r.Repo+"\t"+r.Label)
	}
	ct, cr := C.CString(title), C.CString(strings.Join(rows, "\n"))
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cr))
	vis := C.int(0)
	if v.Visible {
		vis = 1
	}
	C.trayUpdate(vis, codes[v.State], C.double(progress), ct, cr)
}
