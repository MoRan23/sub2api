<template>
  <section
    class="border-t border-gray-200 pt-4 dark:border-dark-600"
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
        class="py-4 first:pt-1"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex min-w-0 items-center gap-2">
            <span
              class="rounded px-2 py-1 text-xs font-medium"
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
            <span class="truncate text-xs font-medium text-gray-600 dark:text-dark-300">
              {{ communicationLabel(communication) }}
            </span>
          </div>
          <time class="text-xs text-gray-500" :datetime="communication.occurred_at">
            {{ formatDate(communication.occurred_at) }}
          </time>
        </div>

        <div class="mt-3 space-y-2 text-sm">
          <div class="text-xs text-gray-500">
            <span v-if="communication.direction === 'outbound'">
              {{ t("admin.groupApplications.to") }}:
              {{ communication.to_address }}
            </span>
            <span v-else>
              {{ t("admin.groupApplications.from") }}:
              {{ communication.from_address }}
            </span>
          </div>
          <h5 v-if="communication.subject" class="font-medium text-gray-900 dark:text-white">
            {{ communication.subject }}
          </h5>

          <div
            v-if="communication.direction === 'outbound' && communication.html_body"
            class="max-h-64 overflow-auto border-l-2 border-gray-200 pl-3 text-sm text-gray-700 dark:border-dark-600 dark:text-dark-200"
            v-html="sanitizeEmailHTML(communication.html_body)"
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
  return DOMPurify.sanitize(value, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: [
      "style",
      "form",
      "iframe",
      "object",
      "embed",
      "img",
      "video",
      "audio",
      "source",
      "link",
      "meta",
    ],
    FORBID_ATTR: ["style"],
  });
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
