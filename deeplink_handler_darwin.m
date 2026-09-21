//go:build darwin && cgo

#import <AppKit/AppKit.h>
#import <Foundation/Foundation.h>

extern void chillHandleURLEvent(char *rawURL);

@interface ChillURLHandler : NSObject
- (void)handleURL:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply;
- (void)timeout:(NSTimer *)timer;
@end

@implementation ChillURLHandler
- (void)handleURL:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
    NSString *url = [[event paramDescriptorForKeyword:keyDirectObject] stringValue];
    if (url != nil) {
        chillHandleURLEvent((char *)[url UTF8String]);
    }
    [NSApp terminate:nil];
}

- (void)timeout:(NSTimer *)timer {
    [NSApp terminate:nil];
}
@end

void chillRunURLHandler(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
        ChillURLHandler *handler = [[ChillURLHandler alloc] init];
        [[NSAppleEventManager sharedAppleEventManager]
            setEventHandler:handler
                andSelector:@selector(handleURL:withReplyEvent:)
              forEventClass:kInternetEventClass
                 andEventID:kAEGetURL];
        [NSTimer scheduledTimerWithTimeInterval:15
                                        target:handler
                                      selector:@selector(timeout:)
                                      userInfo:nil
                                       repeats:NO];
        [NSApp run];
        [[NSAppleEventManager sharedAppleEventManager]
            removeEventHandlerForEventClass:kInternetEventClass
                                  andEventID:kAEGetURL];
        [handler release];
    }
}
