//go:build ios

#import <UIKit/UIKit.h>
#include "host_ios.h"

// The system keyboard is requested by the first tap on this focused editor.
// Leave text selection, scrolling and the editor's own tap handling with UIKit.
@interface RASComposerTap : UITapGestureRecognizer <UIGestureRecognizerDelegate>
@end

@implementation RASComposerTap
- (void)restoreKeyboard:(id)sender {
    UITextView *editor = (UITextView *)self.view;
    editor.inputView = nil;
    if (editor.isFirstResponder) [editor reloadInputViews];
    [[NSNotificationCenter defaultCenter] removeObserver:self];
    [editor removeGestureRecognizer:self];
}
- (BOOL)gestureRecognizer:(UIGestureRecognizer *)gesture shouldRecognizeSimultaneouslyWithGestureRecognizer:(UIGestureRecognizer *)other {
    return YES;
}
@end

static UITextView *composerIn(UIView *view, NSString *entryID, BOOL inEntry, BOOL inField) {
    if (view.hidden || !view.userInteractionEnabled || view.alpha == 0) return nil;
    inEntry |= [view.accessibilityIdentifier isEqualToString:entryID];
    inField |= inEntry && [view.accessibilityIdentifier isEqualToString:@"chat-input"];
    if (inField && [view isKindOfClass:UITextView.class]) return (UITextView *)view;
    for (UIView *child in view.subviews) {
        UITextView *editor = composerIn(child, entryID, inEntry, inField);
        if (editor) return editor;
    }
    return nil;
}

void ras_focus_composer(const char *entry_id) {
    NSString *entryID = [NSString stringWithUTF8String:entry_id];
    void (^focus)(void) = ^{
        for (UIScene *scene in UIApplication.sharedApplication.connectedScenes) {
            if (![scene isKindOfClass:UIWindowScene.class] || scene.activationState != UISceneActivationStateForegroundActive) continue;
            for (UIWindow *window in ((UIWindowScene *)scene).windows) {
                if (!window.isKeyWindow) continue;
                UITextView *editor = composerIn(window, entryID, NO, NO);
                if (!editor.isEditable || editor.isFirstResponder || editor.inputView) continue;
                RASComposerTap *tap = [[RASComposerTap alloc] init];
                [tap addTarget:tap action:@selector(restoreKeyboard:)];
                tap.delegate = tap;
                tap.cancelsTouchesInView = NO;
                tap.delaysTouchesEnded = NO;
                [editor addGestureRecognizer:tap];
                [[NSNotificationCenter defaultCenter] addObserver:tap selector:@selector(restoreKeyboard:)
                    name:UITextViewTextDidEndEditingNotification object:editor];
                editor.inputView = [[[UIView alloc] init] autorelease];
                if (![editor becomeFirstResponder]) [tap restoreKeyboard:nil];
                [tap release];
                return;
            }
        }
    };
    if (NSThread.isMainThread) focus();
    else dispatch_sync(dispatch_get_main_queue(), focus);
}
