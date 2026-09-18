#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>
#import <dispatch/dispatch.h>

// All notification copy is local and fixed. Never accept source paths, log text,
// repository names, or user-controlled titles in this process's arguments.
static NSDictionary<NSString *, NSArray<NSString *> *> *Templates(void) {
    return @{
        @"snapshot-history": @[@"老底：发现含 Git 历史的快照线索",
            @"清单包含历史对象；是否上传成功尚未确认。当前任务继续运行。可在 Agent 中询问“查看老底提醒”。"],
        @"upload-attempt": @[@"老底：发现仓库上传尝试记录",
            @"客户端进入过上传尝试流程；是否发出请求或完成仍未知。当前任务继续运行。可在 Agent 中询问“查看老底提醒”。"],
        @"upload-accepted": @[@"老底：发现上传接受记录",
            @"客户端记录表明上传获确认；远端保存情况未经核验。当前任务继续运行。可在 Agent 中询问“查看老底提醒”。"],
        @"snapshot-workspace": @[@"老底：发现工作区快照清单",
            @"客户端生成了工作区文件清单；未读取文件内容，是否上传或包含秘密尚未知。当前任务继续运行。"],
        @"workspace-upload-attempt": @[@"老底：发现工作区快照上传尝试记录",
            @"客户端进入过相关快照的上传尝试流程；是否发出请求或完成仍未知。当前任务继续运行。"],
        @"workspace-upload-accepted": @[@"老底：发现工作区快照上传接受记录",
            @"客户端记录相关快照获HTTP成功响应；未据此确认包含Git历史或秘密，远端留存未知。当前任务继续运行。"],
        @"snapshot-config": @[@"老底：发现附加配置快照线索",
            @"附加清单列入了全局配置；未读取配置内容，是否包含秘密或完成上传尚未知。当前任务继续运行。"],
        @"config-upload-attempt": @[@"老底：发现附加配置上传尝试记录",
            @"客户端进入过相关快照的上传尝试流程；是否发出请求或完成仍未知。当前任务继续运行。"],
        @"config-upload-accepted": @[@"老底：发现附加配置上传接受记录",
            @"客户端记录相关快照获HTTP成功响应；具体配置内容与远端保存情况未经核验。当前任务继续运行。"],
        @"tool-output-sensitive": @[@"老底：工具输出出现疑似凭据",
            @"已在本机记录风险，提醒不包含具体内容；是否进入模型请求或完成上传尚未确认。当前任务继续运行。"],
        @"hook-coverage-degraded": @[@"老底：工具事件检测存在缺口",
            @"部分事件可能未被完整检查。当前任务继续运行。可在 Agent 中询问“检查老底状态”。"],
        @"coverage-degraded": @[@"老底：部分监测暂不可用",
            @"已支持的监测范围出现缺口。当前任务继续运行。可在 Agent 中询问“检查老底状态”。"]
    };
}

static void WriteJSON(NSDictionary *value) {
    NSData *data = [NSJSONSerialization dataWithJSONObject:value
        options:NSJSONWritingSortedKeys error:NULL];
    if (data) {
        fwrite(data.bytes, 1, data.length, stdout);
        fputc('\n', stdout);
        fflush(stdout);
    }
}

static NSMutableDictionary *Result(NSString *action, BOOL ok) {
    return [@{@"schema_version": @1, @"action": action, @"ok": @(ok),
        @"delivery": @"not_requested", @"task_interrupted": @NO} mutableCopy];
}

static NSString *AuthorizationName(UNAuthorizationStatus status) {
    switch (status) {
        case UNAuthorizationStatusNotDetermined: return @"not_determined";
        case UNAuthorizationStatusDenied: return @"denied";
        case UNAuthorizationStatusAuthorized: return @"authorized";
        case UNAuthorizationStatusProvisional: return @"provisional";
        default: return @"unknown";
    }
}

static NSString *SettingName(UNNotificationSetting setting) {
    switch (setting) {
        case UNNotificationSettingNotSupported: return @"not_supported";
        case UNNotificationSettingDisabled: return @"disabled";
        case UNNotificationSettingEnabled: return @"enabled";
        default: return @"unknown";
    }
}

static void AddSettings(NSMutableDictionary *result, UNNotificationSettings *settings) {
    result[@"authorization"] = AuthorizationName(settings.authorizationStatus);
    result[@"alert_setting"] = SettingName(settings.alertSetting);
    result[@"notification_center_setting"] = SettingName(settings.notificationCenterSetting);
    result[@"lock_screen_setting"] = SettingName(settings.lockScreenSetting);
}

static BOOL ValidIdentifier(NSString *identifier) {
    if (identifier.length < 1 || identifier.length > 64) return NO;
    NSCharacterSet *allowed = [NSCharacterSet characterSetWithCharactersInString:
        @"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"];
    return [identifier rangeOfCharacterFromSet:allowed.invertedSet].location == NSNotFound;
}

static int Fail(NSString *action, NSString *code, int exitCode) {
    NSMutableDictionary *result = Result(action, NO);
    result[@"error_code"] = code;
    WriteJSON(result);
    return exitCode;
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc == 1 || (argc == 2 && strcmp(argv[1], "--help") == 0)) {
            puts("LaodiNotify --status\n"
                 "LaodiNotify --request-permission\n"
                 "LaodiNotify --send --id OPAQUE_ID --kind KIND [--existing]\n"
                 "KIND: snapshot-history | upload-attempt | upload-accepted | snapshot-workspace | workspace-upload-attempt | workspace-upload-accepted | snapshot-config | config-upload-attempt | config-upload-accepted | tool-output-sensitive | hook-coverage-degraded | coverage-degraded\n"
                 "Only --request-permission may request authorization. --send never requests it.");
            return 0;
        }

        NSString *action = [NSString stringWithUTF8String:argv[1]];
        NSString *identifier = nil;
        NSString *kind = nil;
        BOOL existing = NO;
        if (![action isEqualToString:@"--status"] &&
            ![action isEqualToString:@"--request-permission"] &&
            ![action isEqualToString:@"--send"]) {
            return Fail(@"invalid", @"invalid_arguments", 2);
        }
        if ([action isEqualToString:@"--send"]) {
            for (int i = 2; i < argc; ++i) {
                NSString *arg = [NSString stringWithUTF8String:argv[i]];
                if ([arg isEqualToString:@"--id"] && !identifier && i + 1 < argc) {
                    identifier = [NSString stringWithUTF8String:argv[++i]];
                } else if ([arg isEqualToString:@"--kind"] && !kind && i + 1 < argc) {
                    kind = [NSString stringWithUTF8String:argv[++i]];
                } else if ([arg isEqualToString:@"--existing"] && !existing) {
                    existing = YES;
                } else {
                    return Fail(@"send", @"invalid_arguments", 2);
                }
            }
            if (!ValidIdentifier(identifier) || !kind || !Templates()[kind]) {
                return Fail(@"send", @"invalid_arguments", 2);
            }
        } else if (argc != 2) {
            return Fail(@"invalid", @"invalid_arguments", 2);
        }
        action = [action substringFromIndex:2];

        // Validate before creating the notification center. This also keeps
        // invalid CLI invocations and --help entirely outside notification APIs.
        NSBundle *bundle = NSBundle.mainBundle;
        if (![bundle.bundlePath.pathExtension isEqualToString:@"app"] ||
            !bundle.bundleIdentifier.length) {
            return Fail(action, @"app_bundle_required", 3);
        }

        __block BOOL finished = NO;
        __block int exitCode = 0;
        void (^complete)(NSMutableDictionary *, int) = ^(NSMutableDictionary *result, int code) {
            dispatch_async(dispatch_get_main_queue(), ^{
                if (finished) return;
                WriteJSON(result);
                exitCode = code;
                finished = YES;
            });
        };

        @try {
            UNUserNotificationCenter *center = UNUserNotificationCenter.currentNotificationCenter;
            if ([action isEqualToString:@"request-permission"]) {
                [center requestAuthorizationWithOptions:UNAuthorizationOptionAlert
                    completionHandler:^(BOOL granted, NSError *error) {
                    [center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
                        NSMutableDictionary *result = Result(action, error == nil);
                        result[@"granted"] = @(granted);
                        AddSettings(result, settings);
                        if (error) result[@"error_code"] = @"authorization_request_failed";
                        complete(result, error ? 3 : 0);
                    }];
                }];
            } else {
                [center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
                    NSMutableDictionary *result = Result(action, YES);
                    AddSettings(result, settings);
                    if ([action isEqualToString:@"status"]) {
                        complete(result, 0);
                        return;
                    }
                    if (settings.authorizationStatus != UNAuthorizationStatusAuthorized &&
                        settings.authorizationStatus != UNAuthorizationStatusProvisional) {
                        result[@"ok"] = @NO;
                        result[@"delivery"] = @"not_available";
                        result[@"error_code"] = @"notification_not_authorized";
                        complete(result, 3);
                        return;
                    }

                    NSArray<NSString *> *copy = Templates()[kind];
                    UNMutableNotificationContent *content = [UNMutableNotificationContent new];
                    content.title = copy[0];
                    content.body = existing ? [@"安装前已有记录。" stringByAppendingString:copy[1]] : copy[1];
                    content.threadIdentifier = @"laodi-incidents";
                    // Normal nonmodal banner only. No sound, no critical or
                    // time-sensitive entitlement, no Focus bypass, no GUI.
                    content.interruptionLevel = UNNotificationInterruptionLevelActive;
                    content.sound = nil;
                    UNNotificationRequest *request = [UNNotificationRequest
                        requestWithIdentifier:identifier content:content trigger:nil];
                    [center addNotificationRequest:request withCompletionHandler:^(NSError *error) {
                        result[@"ok"] = @(error == nil);
                        result[@"delivery"] = error ? @"not_available" : @"accepted_by_os";
                        if (error) result[@"error_code"] = @"notification_request_failed";
                        complete(result, error ? 3 : 0);
                    }];
                }];
            }

            // Permission is user-controlled. A timeout is unknown, not denial.
            NSTimeInterval timeout = [action isEqualToString:@"request-permission"] ? 60.0 : 10.0;
            NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:timeout];
            while (!finished && deadline.timeIntervalSinceNow > 0) {
                [NSRunLoop.mainRunLoop runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
            }
            if (!finished) {
                finished = YES;
                NSMutableDictionary *result = Result(action, NO);
                result[@"error_code"] = @"notification_service_timeout";
                result[@"delivery"] = [action isEqualToString:@"send"] ? @"unknown" : @"not_requested";
                WriteJSON(result);
                return 3;
            }
            return exitCode;
        } @catch (NSException *exception) {
            (void)exception;
            return Fail(action, @"notification_service_unavailable", 3);
        }
    }
}
