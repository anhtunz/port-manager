// Status item menu bar cho Port Manager — phần Objective-C của package tray
// (mô tả chung xem tray_darwin.go). Được go build biên dịch tự động trên darwin.
//
// LƯU Ý MRC: go build biên dịch .m KHÔNG có ARC. statusItemWithLength: trả về
// object autoreleased — phải retain trước khi giữ trong static, nếu không item
// bị dealloc sau lần đầu autorelease pool xả và icon biến khỏi menu bar.
#import <Cocoa/Cocoa.h>
#import <string.h>

extern void trayActionCB(char* action);

@interface TrayTarget : NSObject
- (void)onMenu:(NSMenuItem*)sender;
@end

@implementation TrayTarget
- (void)onMenu:(NSMenuItem*)sender {
    NSString* act = sender.representedObject;
    if (act.length == 0) {
        return;
    }
    char* c = strdup(act.UTF8String);
    if (c != NULL) {
        trayActionCB(c); // phía Go: C.GoString rồi C.free
    }
}
@end

static NSStatusItem* gItem = nil;
static TrayTarget* gTarget = nil;

static void traySetupMain(NSData* icon, NSString* tooltip) {
    if (gItem != nil) {
        return;
    }
    gItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
    gTarget = [[TrayTarget alloc] init];

    NSImage* img = [[NSImage alloc] initWithData:icon];
    if (img != nil) {
        img.size = NSMakeSize(18, 18); // PNG 36px @2x → 18pt trên menu bar
        img.template = YES;            // đen + alpha → hệ tự đảo màu theo sáng/tối
        gItem.button.image = img;
        [img release]; // button đã retain
    }
    gItem.button.toolTip = tooltip;

    NSMenu* menu = [[NSMenu alloc] init];
    NSArray* titles = @[@"Mở Port Manager", @"Quét lại", @"Bật tất cả", @"Tắt tất cả", @"Giới thiệu"];
    NSArray* actions = @[@"open", @"refresh", @"start-all", @"stop-all", @"about"];
    for (NSUInteger i = 0; i < titles.count; i++) {
        NSMenuItem* it = [[NSMenuItem alloc] initWithTitle:titles[i]
                                                    action:@selector(onMenu:)
                                             keyEquivalent:@""];
        it.target = gTarget;
        it.representedObject = actions[i];
        [menu addItem:it];
        [it release]; // menu đã retain
        if (i == 0 || i == 3) {
            [menu addItem:[NSMenuItem separatorItem]];
        }
    }
    NSMenuItem* quit = [[NSMenuItem alloc] initWithTitle:@"Thoát Port Manager"
                                                  action:@selector(onMenu:)
                                           keyEquivalent:@""];
    quit.target = gTarget;
    quit.representedObject = @"quit";
    [menu addItem:quit];
    [quit release];
    gItem.menu = menu; // bấm icon là xổ menu ngay
    [menu release];    // gItem đã retain

    // gItem + gTarget sống cùng app (không có removeStatusItem) → không release.
}

void traySetup(const void* png, long len, const char* tooltip) {
    // Copy byte icon TRƯỚC khi rời thread của Go — pointer chỉ hợp lệ trong lúc gọi.
    NSData* data = [NSData dataWithBytes:png length:(NSUInteger)len];
    NSString* tip = tooltip != NULL ? [NSString stringWithUTF8String:tooltip] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        traySetupMain(data, tip);
    });
}
