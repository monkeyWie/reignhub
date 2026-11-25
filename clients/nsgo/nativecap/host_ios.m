//go:build ios

#import <UIKit/UIKit.h>
#include "host_ios.h"
#include <sys/utsname.h>

char *ras_device_name(void) {
    __block char *name=NULL;
    void (^read)(void)=^{ name=strdup([UIDevice currentDevice].name.UTF8String); };
    if([NSThread isMainThread]) read(); else dispatch_sync(dispatch_get_main_queue(),read);
    return name;
}

char *ras_timezone_name(void) { return strdup([NSTimeZone localTimeZone].name.UTF8String); }
int ras_timezone_offset(void) { return (int)[[NSTimeZone localTimeZone] secondsFromGMT]; }

char *ras_device_model(void) {
    const char *simulator = getenv("SIMULATOR_MODEL_IDENTIFIER");
    if (simulator && *simulator) return strdup(simulator);
    struct utsname system;
    if (uname(&system) == 0) return strdup(system.machine);
    return strdup("");
}
