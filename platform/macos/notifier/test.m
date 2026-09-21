#define main LaodiNotifierMain
#include "main.m"
#undef main

static void Check(BOOL condition, NSString *message) {
    if (!condition) {
        fprintf(stderr, "%s\n", message.UTF8String);
        exit(1);
    }
}

int main(void) {
    @autoreleasepool {
        NSDictionary *templates = Templates();
        Check([templates[@"archive-blocked-test"] isEqual:@[@"测试打包操作已拦截",
            @"Git 历史打包保护正常。"]], @"protection test must be labelled");
        Check(templates[@"untrusted-kind"] == nil, @"unknown kind accepted");
        Check(!ValidIdentifier(@"path/secret") && ValidIdentifier(@"opaque_123"),
            @"identifier validation failed");

        NSBundle *bundle = NSBundle.mainBundle;
        NSString *icon = [bundle objectForInfoDictionaryKey:@"CFBundleIconFile"];
        Check([icon isEqualToString:@"Laodi.icns"], @"incorrect app icon reference");
        NSData *iconData = [NSData dataWithContentsOfURL:
            [bundle URLForResource:@"Laodi" withExtension:@"icns"]];
        Check(iconData.length > 8 && memcmp(iconData.bytes, "icns", 4) == 0,
            @"app icon missing or invalid");
        Check([bundle URLForResource:@"LaodiNotification" withExtension:@"png"] == nil,
            @"obsolete notification attachment still packaged");
        puts("PASS: fixed protection-test copy, identifiers and app icon resource");
    }
    return 0;
}
