<template>
  <section
    class="min-w-0 border-t border-gray-200 pt-4 dark:border-dark-600"
    aria-labelledby="group-application-communications-heading"
  >
    <div class="mb-3 flex items-center justify-between gap-3">
      <h4
        id="group-application-communications-heading"
        class="text-sm font-semibold"
      >
        {{ t("admin.groupApplications.emailHistory") }}
      </h4>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="btn btn-secondary px-2 py-1.5"
          :disabled="loading"
          :title="t('admin.groupApplications.refreshEmailHistory')"
          :aria-label="t('admin.groupApplications.refreshEmailHistory')"
          @click="emit('refresh')"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
        <button
          type="button"
          class="btn btn-secondary px-2 py-1.5"
          :disabled="loading || !communications.length"
          :title="t('admin.groupApplications.exportEmailHistory')"
          :aria-label="t('admin.groupApplications.exportEmailHistory')"
          @click="emit('export')"
        >
          <Icon name="download" size="sm" />
        </button>
      </div>
    </div>

    <p v-if="loading" class="py-6 text-center text-sm text-gray-500">
      {{ t("common.loading") }}
    </p>
    <p
      v-else-if="!communications.length"
      class="py-6 text-center text-sm text-gray-500"
    >
      {{ t("admin.groupApplications.noEmailHistory") }}
    </p>
    <ol v-else class="divide-y divide-gray-200 dark:divide-dark-700">
      <li
        v-for="communication in communications"
        :key="`${communication.direction}-${communication.id}`"
        class="min-w-0 py-4 first:pt-1"
      >
        <div
          class="flex min-w-0 flex-col items-start gap-2 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="flex min-w-0 max-w-full items-center gap-2">
            <span
              class="shrink-0 rounded px-2 py-1 text-xs font-medium"
              :class="
                communication.direction === 'outbound'
                  ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
                  : 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
              "
            >
              {{
                communication.direction === "outbound"
                  ? t("admin.groupApplications.outboundEmail")
                  : t("admin.groupApplications.inboundEmail")
              }}
            </span>
            <span
              class="min-w-0 truncate text-xs font-medium text-gray-600 dark:text-dark-300"
            >
              {{ communicationLabel(communication) }}
            </span>
          </div>
          <time
            class="shrink-0 text-xs text-gray-500"
            :datetime="communication.occurred_at"
          >
            {{ formatDate(communication.occurred_at) }}
          </time>
        </div>

        <div class="mt-3 min-w-0 space-y-2 text-sm">
          <div class="min-w-0 break-all text-xs text-gray-500">
            <span v-if="communication.direction === 'outbound'">
              {{ t("admin.groupApplications.to") }}:
              {{ communication.to_address }}
            </span>
            <span v-else>
              {{ t("admin.groupApplications.from") }}:
              {{ communication.from_address }}
            </span>
          </div>
          <h5
            v-if="communication.subject"
            class="break-all font-medium text-gray-900 dark:text-white"
          >
            {{ communication.subject }}
          </h5>

          <iframe
            v-if="communication.direction === 'outbound' && communication.html_body"
            class="h-80 w-full rounded border border-gray-200 bg-white dark:border-dark-600"
            :srcdoc="sanitizeEmailHTML(communication.html_body)"
            sandbox=""
            referrerpolicy="no-referrer"
            :title="t('admin.groupApplications.emailPreview')"
          />
          <template v-else-if="communication.direction === 'inbound'">
            <p
              v-if="communication.content_unavailable"
              class="text-sm text-amber-700 dark:text-amber-300"
            >
              {{ t("admin.groupApplications.emailContentUnavailable") }}
            </p>
            <pre
              v-else
              class="max-h-64 overflow-auto whitespace-pre-wrap break-words border-l-2 border-gray-200 pl-3 font-sans text-sm text-gray-700 dark:border-dark-600 dark:text-dark-200"
            >{{ communication.text_body }}</pre>
            <p
              v-if="communication.content_truncated"
              class="text-xs text-amber-700 dark:text-amber-300"
            >
              {{ t("admin.groupApplications.emailContentTruncated") }}
            </p>
          </template>

          <p v-if="communication.attachment_name" class="text-xs text-gray-500">
            {{ t("admin.groupApplications.attachment") }}:
            {{ communication.attachment_name }}
            <span v-if="communication.attachment_size">
              ({{ formatBytes(communication.attachment_size) }})
            </span>
          </p>
          <p v-if="communication.message_id" class="break-all font-mono text-xs text-gray-400">
            Message-ID: {{ communication.message_id }}
          </p>
          <p v-if="communication.last_error" class="text-xs text-red-600">
            {{ communication.last_error }}
          </p>

          <div
            v-if="communication.direction === 'outbound'"
            class="flex items-center justify-between gap-3 text-xs text-gray-500"
          >
            <span>
              {{ communication.status }}
              <template v-if="communication.reply_status">
                · {{ t(`admin.groupApplications.replyStatuses.${communication.reply_status}`) }}
              </template>
              ·
              {{ t("admin.groupApplications.attempts", { count: communication.attempts || 0 }) }}
            </span>
            <button
              v-if="communication.retryable"
              type="button"
              class="btn btn-secondary px-2 py-1 text-xs"
			  :disabled="actionsDisabled"
              @click="emit('retry', communication.id)"
            >
              {{ t("common.retry") }}
            </button>
          </div>
        </div>
      </li>
    </ol>
  </section>
</template>

<script setup lang="ts">
import DOMPurify from "dompurify";
import { useI18n } from "vue-i18n";
import Icon from "@/components/icons/Icon.vue";
import type { GroupApplicationCommunication } from "@/api/admin/groupApplications";

withDefaults(
  defineProps<{
    communications: GroupApplicationCommunication[];
    loading?: boolean;
	actionsDisabled?: boolean;
  }>(),
	{ loading: false, actionsDisabled: false },
);

const emit = defineEmits<{
  export: [];
  refresh: [];
  retry: [outboxID: number];
}>();
const { t } = useI18n();

function communicationLabel(communication: GroupApplicationCommunication) {
  if (communication.kind) {
    return t(`admin.groupApplications.templateKinds.${communication.kind}`);
  }
  if (communication.result) {
    return t(`admin.groupApplications.communicationResults.${communication.result}`);
  }
  return t("admin.groupApplications.inboundEmail");
}

function sanitizeEmailHTML(value: string) {
  const sanitized = DOMPurify.sanitize(value, {
    USE_PROFILES: { html: true },
    WHOLE_DOCUMENT: true,
    FORBID_TAGS: [
      "script",
      "form",
      "input",
      "button",
      "textarea",
      "select",
      "option",
      "iframe",
      "object",
      "embed",
      "video",
      "audio",
      "source",
      "track",
      "link",
      "base",
      "meta",
    ],
    FORBID_ATTR: [
      "action",
      "background",
      "download",
      "formaction",
      "href",
      "ping",
      "poster",
      "srcset",
      "target",
      "xlink:href",
    ],
  });

  const previewDocument = new DOMParser().parseFromString(sanitized, "text/html");
  previewDocument.querySelectorAll("img[src]").forEach((image) => {
    const source = image.getAttribute("src")?.trim() ?? "";
    if (!/^data:image\/(?:avif|gif|jpe?g|png|webp);base64,/i.test(source)) {
      image.removeAttribute("src");
    }
  });

  const contentSecurityPolicy = previewDocument.createElement("meta");
  contentSecurityPolicy.httpEquiv = "Content-Security-Policy";
  contentSecurityPolicy.content = [
    "default-src 'none'",
    "style-src 'unsafe-inline'",
    "img-src data:",
    "font-src data:",
    "media-src 'none'",
    "connect-src 'none'",
    "frame-src 'none'",
    "object-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
  ].join("; ");
  previewDocument.head.prepend(contentSecurityPolicy);

  return `<!doctype html>\n${previewDocument.documentElement.outerHTML}`;
}

function formatDate(value: string) {
  return new Date(value).toLocaleString();
}

function formatBytes(value: number) {
  return value < 1024 * 1024
    ? `${Math.round(value / 1024)} KiB`
    : `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
</script>
