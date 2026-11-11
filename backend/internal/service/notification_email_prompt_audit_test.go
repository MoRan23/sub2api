package service

import (
	"context"
	"html"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotificationEmailPromptAuditTemplates(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, event := range []string{NotificationEmailEventPromptAuditWarning, NotificationEmailEventPromptAuditBanned} {
		t.Run(event, func(t *testing.T) {
			info, normalized, err := svc.eventInfo(event)
			require.NoError(t, err)
			require.Equal(t, event, normalized)
			require.False(t, info.Optional)
			require.Contains(t, svc.ListEventInfos(), info)
			require.ElementsMatch(t, append(append([]string{}, notificationEmailCommonPlaceholders...), notificationEmailPromptAuditPlaceholders...), info.Placeholders)
			for _, locale := range []string{"en", "zh-CN"} {
				template, err := svc.GetTemplate(context.Background(), event, locale)
				require.NoError(t, err)
				for _, key := range notificationEmailPromptAuditPlaceholders {
					require.Contains(t, template.HTML, "{{"+key+"}}")
				}
				preview, err := svc.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{Event: event, Locale: locale})
				require.NoError(t, err)
				require.NotContains(t, preview.HTML, "{{")
				require.NotContains(t, preview.HTML, "unsubscribe")
				require.Contains(t, preview.HTML, "24")
				require.Contains(t, preview.HTML, "10")
			}
		})
	}
}

func TestNotificationEmailPromptAuditEscapesEvidenceFreeSummary(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, event := range []string{NotificationEmailEventPromptAuditWarning, NotificationEmailEventPromptAuditBanned} {
		for _, locale := range []string{"en", "zh"} {
			variables := make(map[string]string)
			for _, key := range notificationEmailPromptAuditPlaceholders {
				variables[key] = `<img src=x onerror="test">` + key
			}
			preview, err := svc.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{Event: event, Locale: locale, Variables: variables})
			require.NoError(t, err)
			for _, value := range variables {
				require.Contains(t, preview.HTML, html.EscapeString(value))
			}
			require.NotContains(t, preview.HTML, "<img")
			for _, prohibited := range []string{"prompt", "matched_keyword", "matched_content", "regex", "api_key", "rule"} {
				_, err := svc.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{
					Event: event, Locale: locale, Subject: "Notice", HTML: "<p>{{" + prohibited + "}}</p>",
				})
				require.ErrorContains(t, err, "unsupported placeholder")
			}
		}
	}
}

func TestNotificationEmailPromptAuditRuntimeDoesNotUseSampleCounts(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, event := range []string{NotificationEmailEventPromptAuditWarning, NotificationEmailEventPromptAuditBanned} {
		values := svc.runtimeVariables(context.Background(), event, "en", NotificationEmailSendInput{Variables: map[string]string{"new_hit_count": "7"}})
		require.Equal(t, "7", values["new_hit_count"])
		for _, key := range notificationEmailPromptAuditPlaceholders {
			if key != "new_hit_count" {
				require.Empty(t, values[key], key)
			}
		}
	}
}
