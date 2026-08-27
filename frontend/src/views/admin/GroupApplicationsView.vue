<template>
  <AppLayout>
    <div class="mx-auto w-full max-w-7xl space-y-6 pb-8">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
            {{ t("admin.groupApplications.title") }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
            {{ t("admin.groupApplications.description") }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary self-start px-3 sm:self-auto"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          :disabled="loading"
          @click="reloadActiveTab"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
      </header>

      <nav class="border-b border-gray-200 dark:border-dark-700" aria-label="Group applications">
        <div class="flex gap-6 overflow-x-auto" role="tablist">
          <button
            v-for="item in tabs"
            :key="item.value"
            type="button"
            role="tab"
            :aria-selected="activeTab === item.value"
            class="whitespace-nowrap border-b-2 px-1 py-3 text-sm font-medium transition-colors"
            :class="
              activeTab === item.value
                ? 'border-primary-500 text-primary-600 dark:text-primary-400'
                : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-100'
            "
            @click="activeTab = item.value"
          >
            {{ item.label }}
          </button>
        </div>
      </nav>

      <section v-if="activeTab === 'applications'" class="card overflow-hidden">
        <div class="flex flex-col gap-3 border-b border-gray-200 p-4 dark:border-dark-700 sm:flex-row">
          <SearchInput
            v-model="filters.search"
            class="sm:max-w-md"
            :placeholder="t('admin.groupApplications.search')"
            @search="applyFilters"
          />
          <div class="w-full sm:w-52">
            <Select
              :model-value="filters.status"
              :options="statusOptions"
              :searchable="false"
			  :aria-label="t('admin.groupApplications.allStatuses')"
              @update:model-value="setStatusFilter"
            />
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th
                  v-for="heading in applicationHeadings"
                  :key="heading"
                  class="px-4 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-dark-400"
                >
                  {{ heading }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
              <tr v-if="loading">
                <td :colspan="7" class="px-4 py-12 text-center text-sm text-gray-500">
                  {{ t("common.loading") }}
                </td>
              </tr>
              <tr
                v-for="item in applications"
                v-else
                :key="item.id"
                class="cursor-pointer transition-colors hover:bg-gray-50 dark:hover:bg-dark-800"
                @click="openApplication(item.id)"
              >
                <td class="whitespace-nowrap px-4 py-3 text-sm font-medium">#{{ item.id }}</td>
                <td class="px-4 py-3 text-sm">
                  <div class="font-medium text-gray-900 dark:text-white">{{ item.user_email }}</div>
                  <div class="text-xs text-gray-500">UID {{ item.user_id }}</div>
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-sm">{{ item.group_name }}</td>
                <td class="px-4 py-3 text-sm">{{ item.contact_email }}</td>
                <td class="whitespace-nowrap px-4 py-3">
                  <span :class="statusClass(item.status)" class="rounded px-2 py-1 text-xs font-medium">
                    {{ t(`groupApplications.status.${item.status}`) }}
                  </span>
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-sm text-gray-500">
                  {{ formatDate(item.created_at) }}
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-xs">
                  <span
                    v-if="item.last_email_status"
                    :class="item.last_email_status === 'failed' ? 'text-red-600' : 'text-gray-500'"
                  >
                    {{ item.last_email_status }}
                  </span>
                </td>
              </tr>
              <tr v-if="!loading && !applications.length">
                <td :colspan="7" class="px-4 py-14 text-center text-sm text-gray-500">
                  <Icon name="inbox" size="lg" class="mx-auto mb-3 text-gray-300 dark:text-dark-500" />
                  {{ t("admin.groupApplications.noApplications") }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          v-if="totalApplications > pageSize"
          :total="totalApplications"
          :page="page"
          :page-size="pageSize"
          :show-page-size-selector="false"
          @update:page="changePage"
        />
      </section>

      <section
        v-else-if="activeTab === 'policies'"
        class="grid items-start gap-6 lg:grid-cols-[300px_minmax(0,1fr)]"
      >
        <aside class="card p-4">
          <label class="input-label mb-1.5 block">
            {{ t("admin.groupApplications.applyableGroup") }}
          </label>
          <Select
            :model-value="selectedGroupID || null"
            :options="eligibleGroupOptions"
            :placeholder="t('groupApplications.selectGroup')"
			:aria-label="t('admin.groupApplications.applyableGroup')"
            searchable
            @update:model-value="setSelectedGroup"
          />
          <div class="mt-4 space-y-1 border-t border-gray-200 pt-4 dark:border-dark-700">
            <button
              v-for="policy in policies"
              :key="policy.group_id"
              type="button"
              class="flex w-full items-center justify-between rounded px-3 py-2 text-left text-sm transition-colors"
              :class="
                selectedGroupID === policy.group_id
                  ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
                  : 'hover:bg-gray-100 dark:hover:bg-dark-800'
              "
              @click="selectPolicy(policy.group_id)"
            >
              <span class="truncate">{{ policy.group_name }}</span>
              <span
                class="ml-3 h-2 w-2 flex-none rounded-full"
                :class="policy.enabled ? 'bg-green-500' : 'bg-gray-300 dark:bg-dark-500'"
              />
            </button>
          </div>
        </aside>

        <div v-if="policyForm" class="card p-5 sm:p-6">
          <div class="flex flex-col gap-4 border-b border-gray-200 pb-5 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">
                {{ policyForm.group_name }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t("admin.groupApplications.policyDescription") }}
              </p>
            </div>
            <div class="flex items-center gap-3">
              <span class="text-sm font-medium">{{ t("admin.groupApplications.enabled") }}</span>
              <Toggle v-model="policyForm.enabled" />
            </div>
          </div>

          <div class="mt-6 grid gap-5 md:grid-cols-2">
            <Input
              v-model="policyForm.reply_phrase"
              :label="t('admin.groupApplications.replyPhrase')"
              :hint="t('admin.groupApplications.replyPhraseHint')"
			  :maxlength="500"
            />
            <div>
              <label class="input-label mb-1.5 block">
                {{ t("admin.groupApplications.pdfAgreement") }}
              </label>
              <input
                ref="fileInput"
                type="file"
                accept="application/pdf,.pdf"
                class="sr-only"
                @change="handleAttachment"
              />
              <div class="flex min-h-16 items-center gap-3 rounded border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-600 dark:bg-dark-900">
                <span class="rounded bg-white p-2 text-gray-500 shadow-sm dark:bg-dark-800 dark:text-dark-300">
                  <Icon name="document" size="md" />
                </span>
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm font-medium text-gray-900 dark:text-white">
                    {{ attachment?.name || policyForm.attachment_name || t("admin.groupApplications.noPDF") }}
                  </p>
                  <p v-if="attachment || policyForm.attachment_size" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                    {{ formatBytes(attachment?.size || policyForm.attachment_size || 0) }}
                  </p>
                </div>
                <div class="flex flex-none items-center gap-2">
                  <button
                    type="button"
                    class="btn btn-secondary px-3"
                    :title="t('admin.groupApplications.choosePDF')"
                    @click="fileInput?.click()"
                  >
                    <Icon name="upload" size="sm" />
                  </button>
                  <button
                    v-if="policyForm.attachment_id"
                    type="button"
                    class="btn btn-secondary px-3"
                    :title="t('admin.groupApplications.downloadAgreement')"
                    @click="downloadAgreement"
                  >
                    <Icon name="download" size="sm" />
                  </button>
                </div>
              </div>
            </div>
          </div>

          <GroupApplicationTemplateEditor v-model="policyForm.templates" />
          <div class="mt-6 flex justify-end border-t border-gray-200 pt-5 dark:border-dark-700">
            <button type="button" class="btn btn-primary" :disabled="saving" @click="savePolicy">
              <Icon name="check" size="sm" class="mr-2" />
              {{ t("common.save") }}
            </button>
          </div>
        </div>
        <div v-else class="card px-6 py-16 text-center text-sm text-gray-500">
          {{ t("admin.groupApplications.selectPolicyHint") }}
        </div>
      </section>

      <section v-else-if="emailForm" class="space-y-6">
        <div class="flex flex-col gap-4 rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800 sm:flex-row sm:items-center sm:justify-between">
          <div class="flex items-start gap-3">
            <div class="mt-0.5 rounded bg-primary-50 p-2 text-primary-600 dark:bg-primary-900/20 dark:text-primary-300">
              <Icon name="mail" size="md" />
            </div>
            <div>
              <h2 class="font-semibold text-gray-900 dark:text-white">
                {{ t("admin.groupApplications.emailModule") }}
              </h2>
              <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-dark-400">
                {{ t("admin.groupApplications.emailModuleHint") }}
              </p>
              <p class="mt-2 flex items-center gap-2 text-xs" :class="workflowStatusClass">
                <span class="h-2 w-2 rounded-full bg-current" />{{ workflowStatusText }}
              </p>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <span class="text-sm font-medium">
              {{ emailForm.enabled ? t("common.enabled") : t("common.disabled") }}
            </span>
            <Toggle v-model="emailForm.enabled" />
          </div>
        </div>

        <div
          v-if="emailForm.legacy_imported"
          class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800/60 dark:bg-amber-900/20 dark:text-amber-200"
        >
          {{ t("admin.groupApplications.legacyImported") }}
        </div>
        <div
          v-if="workerHealth?.configuration_error"
          class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800/60 dark:bg-red-900/20 dark:text-red-200"
        >
          {{ workerHealth.configuration_error }}
        </div>

        <div class="grid items-start gap-6 xl:grid-cols-2">
          <article class="card p-5 sm:p-6">
            <div class="mb-5 border-b border-gray-200 pb-4 dark:border-dark-700">
              <h3 class="font-semibold text-gray-900 dark:text-white">
                {{ t("admin.groupApplications.smtpTitle") }}
              </h3>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t("admin.groupApplications.smtpHint") }}
              </p>
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <Input v-model="emailForm.smtp.host" label="Host" placeholder="smtp.example.com" />
              <label class="block">
                <span class="input-label mb-1.5 block">Port</span>
                <input v-model.number="emailForm.smtp.port" class="input w-full" type="number" min="1" max="65535" />
              </label>
              <Input
                v-model="emailForm.smtp.username"
                :label="t('admin.groupApplications.username')"
                autocomplete="username"
              />
              <Input
                v-model="emailForm.smtp.password"
                type="password"
                :label="t('admin.groupApplications.password')"
                autocomplete="new-password"
                :placeholder="emailForm.smtp.password_configured ? '********' : ''"
                :hint="passwordHint(emailForm.smtp.password_configured)"
              />
              <Input
                v-model="emailForm.smtp.from_address"
                type="email"
                :label="t('admin.groupApplications.senderAddress')"
                placeholder="applications@example.com"
              />
              <Input
                v-model="emailForm.smtp.from_name"
                :label="t('admin.groupApplications.senderName')"
              />
              <div class="sm:col-span-2">
                <label class="input-label mb-1.5 block">
                  {{ t("admin.groupApplications.connectionEncryption") }}
                </label>
                <Select
                  :model-value="emailForm.smtp.tls_mode"
                  :options="tlsOptions"
                  :searchable="false"
				  :aria-label="t('admin.groupApplications.smtpEncryption')"
                  @update:model-value="setSMTPTLSMode"
                />
                <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
                  {{ tlsHelp(emailForm.smtp.tls_mode, "smtp") }}
                </p>
              </div>
            </div>
            <div class="mt-6 space-y-3 border-t border-gray-200 pt-5 dark:border-dark-700">
              <Input
                v-model="testRecipient"
                type="email"
                :label="t('admin.groupApplications.testRecipient')"
                placeholder="admin@example.com"
              />
              <div class="flex flex-wrap gap-2">
                <button
                  type="button"
                  class="btn btn-secondary"
                  :disabled="testingAction !== null"
                  @click="testSMTP"
                >
                  <Icon name="shield" size="sm" class="mr-2" />
                  {{ t("admin.groupApplications.testSMTP") }}
                </button>
                <button
                  type="button"
                  class="btn btn-secondary"
                  :disabled="testingAction !== null || !testRecipient"
                  @click="sendTestEmail"
                >
                  <Icon name="mail" size="sm" class="mr-2" />
                  {{ t("admin.groupApplications.sendTest") }}
                </button>
              </div>
            </div>
          </article>

          <article class="card p-5 sm:p-6">
            <div class="mb-5 border-b border-gray-200 pb-4 dark:border-dark-700">
              <h3 class="font-semibold text-gray-900 dark:text-white">
                {{ t("admin.groupApplications.imapTitle") }}
              </h3>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t("admin.groupApplications.imapHint") }}
              </p>
            </div>
            <div class="mb-5 flex items-center justify-between gap-4 rounded-lg bg-gray-50 px-4 py-3 dark:bg-dark-900">
              <div>
                <div class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t("admin.groupApplications.reuseSMTPCredentials") }}
                </div>
                <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                  {{ t("admin.groupApplications.reuseSMTPCredentialsHint") }}
                </div>
              </div>
              <Toggle v-model="emailForm.imap.use_smtp_credentials" />
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <Input v-model="emailForm.imap.host" label="Host" placeholder="imap.example.com" />
              <label class="block">
                <span class="input-label mb-1.5 block">Port</span>
                <input v-model.number="emailForm.imap.port" class="input w-full" type="number" min="1" max="65535" />
              </label>
              <Input
                v-model="emailForm.imap.username"
                :label="t('admin.groupApplications.username')"
                autocomplete="username"
                :disabled="emailForm.imap.use_smtp_credentials"
                :placeholder="emailForm.imap.use_smtp_credentials ? emailForm.smtp.username : ''"
              />
              <Input
                v-model="emailForm.imap.password"
                type="password"
                :label="t('admin.groupApplications.password')"
                autocomplete="new-password"
                :disabled="emailForm.imap.use_smtp_credentials"
                :placeholder="emailForm.imap.password_configured ? '********' : ''"
                :hint="passwordHint(emailForm.imap.password_configured)"
              />
              <Input
                v-model="emailForm.imap.reply_address"
                type="email"
                :label="t('admin.groupApplications.replyAddress')"
                placeholder="applications@example.com"
              />
              <div>
                <label class="input-label mb-1.5 block">
                  {{ t("admin.groupApplications.mailbox") }}
                </label>
                <Select
                  v-model="emailForm.imap.mailbox"
                  :options="mailboxOptions"
                  :placeholder="t('admin.groupApplications.mailboxPlaceholder')"
				  :aria-label="t('admin.groupApplications.mailbox')"
                  searchable
                  creatable
                />
                <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
                  {{ t("admin.groupApplications.mailboxHint") }}
                </p>
              </div>
              <div>
                <label class="input-label mb-1.5 block">
                  {{ t("admin.groupApplications.connectionEncryption") }}
                </label>
                <Select
                  :model-value="emailForm.imap.tls_mode"
                  :options="tlsOptions"
                  :searchable="false"
				  :aria-label="t('admin.groupApplications.imapEncryption')"
                  @update:model-value="setIMAPTLSMode"
                />
                <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
                  {{ tlsHelp(emailForm.imap.tls_mode, "imap") }}
                </p>
              </div>
              <label class="block">
                <span class="input-label mb-1.5 block">
                  {{ t("admin.groupApplications.pollInterval") }}
                </span>
                <input
                  v-model.number="emailForm.imap.poll_interval_seconds"
                  class="input w-full"
                  type="number"
                  min="15"
                  max="300"
                />
              </label>
            </div>
            <div class="mt-6 border-t border-gray-200 pt-5 dark:border-dark-700">
              <button
                type="button"
                class="btn btn-secondary"
                :disabled="testingAction !== null"
                @click="testIMAP"
              >
                <Icon name="inbox" size="sm" class="mr-2" />
                {{ t("admin.groupApplications.testIMAP") }}
              </button>
            </div>
          </article>
        </div>

        <div class="flex justify-end border-t border-gray-200 pt-5 dark:border-dark-700">
          <button type="button" class="btn btn-primary" :disabled="saving" @click="saveEmailConfig">
            <Icon name="check" size="sm" class="mr-2" />
            {{ t("admin.groupApplications.saveEmailConfig") }}
          </button>
        </div>
      </section>

      <div v-else class="py-16 text-center text-sm text-gray-500">
        {{ t("common.loading") }}
      </div>
    </div>

    <BaseDialog
      :show="Boolean(selectedApplication)"
      :title="selectedApplication ? `#${selectedApplication.id} ${selectedApplication.group_name}` : ''"
      width="wide"
      @close="closeApplication"
    >
      <div v-if="selectedApplication" class="space-y-5">
        <dl class="grid gap-4 text-sm sm:grid-cols-2">
          <div>
            <dt class="text-gray-500">{{ t("admin.groupApplications.user") }}</dt>
            <dd class="mt-1 font-medium">
              {{ selectedApplication.user_email }} ({{ selectedApplication.user_id }})
            </dd>
          </div>
          <div>
            <dt class="text-gray-500">{{ t("groupApplications.contactEmail") }}</dt>
            <dd class="mt-1 font-medium">{{ selectedApplication.contact_email }}</dd>
          </div>
        </dl>
        <div>
          <div class="text-sm text-gray-500">{{ t("groupApplications.reason") }}</div>
          <p class="mt-1 whitespace-pre-wrap text-sm">{{ selectedApplication.reason }}</p>
        </div>
        <div v-if="selectedApplication.decision_reason" class="text-sm text-red-600">
          {{ selectedApplication.decision_reason }}
        </div>
        <GroupApplicationCommunicationTimeline
          :communications="communications"
          :loading="communicationsLoading"
          :actions-disabled="!emailForm?.enabled"
          @export="exportCommunications"
          @retry="retryMail"
        />
      </div>
      <template #footer>
        <div class="flex w-full flex-wrap justify-end gap-2">
          <button
            v-if="selectedApplication?.status === 'pending'"
            type="button"
            class="btn btn-primary"
            :disabled="saving || !emailForm?.enabled"
            @click="approveSelected"
          >
            <Icon name="check" size="sm" class="mr-2" />{{ t("admin.groupApplications.approve") }}
          </button>
          <button
            v-if="selectedApplication?.status === 'pending' || selectedApplication?.status === 'awaiting_reply'"
            type="button"
            class="btn btn-danger"
            :disabled="saving || !emailForm?.enabled"
            @click="openDecision('reject')"
          >
            <Icon name="x" size="sm" class="mr-2" />{{ t("admin.groupApplications.reject") }}
          </button>
          <button
            v-if="selectedApplication?.status === 'awaiting_reply'"
            type="button"
            class="btn btn-secondary"
            :disabled="saving || !emailForm?.enabled"
            @click="resendApproval"
          >
            <Icon name="mail" size="sm" class="mr-2" />{{ t("admin.groupApplications.resendApproval") }}
          </button>
          <button
            v-if="selectedApplication?.status === 'completed'"
            type="button"
            class="btn btn-danger"
            :disabled="saving || !emailForm?.enabled"
            @click="openDecision('revoke')"
          >
            {{ t("admin.groupApplications.revoke") }}
          </button>
          <button type="button" class="btn btn-secondary" @click="closeApplication">
            {{ t("common.close") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="decisionMode !== null"
      :title="decisionMode === 'reject' ? t('admin.groupApplications.reject') : t('admin.groupApplications.revoke')"
      width="narrow"
      @close="decisionMode = null"
    >
      <TextArea
        v-model="decisionReason"
        :label="t('admin.groupApplications.decisionReason')"
        :rows="5"
		:maxlength="2000"
      />
      <template #footer>
        <button type="button" class="btn btn-secondary" @click="decisionMode = null">
          {{ t("common.cancel") }}
        </button>
        <button
          type="button"
          class="btn btn-danger"
          :disabled="!decisionReason.trim() || saving"
          @click="submitDecision"
        >
          {{ t("common.confirm") }}
        </button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import AppLayout from "@/components/layout/AppLayout.vue";
import BaseDialog from "@/components/common/BaseDialog.vue";
import Input from "@/components/common/Input.vue";
import Pagination from "@/components/common/Pagination.vue";
import SearchInput from "@/components/common/SearchInput.vue";
import Select from "@/components/common/Select.vue";
import TextArea from "@/components/common/TextArea.vue";
import Toggle from "@/components/common/Toggle.vue";
import Icon from "@/components/icons/Icon.vue";
import GroupApplicationTemplateEditor from "@/components/admin/groupApplications/GroupApplicationTemplateEditor.vue";
import GroupApplicationCommunicationTimeline from "@/components/admin/groupApplications/GroupApplicationCommunicationTimeline.vue";
import { adminAPI } from "@/api";
import { useAppStore } from "@/stores/app";
import type { AdminGroup } from "@/types";
import { extractI18nErrorMessage } from "@/utils/apiError";
import {
  groupApplicationsAdminAPI,
  defaultGroupApplicationTemplates,
  type AdminGroupApplication,
  type GroupApplicationCommunication,
  type GroupApplicationEmailConfig,
  type GroupApplicationPolicy,
  type GroupApplicationTLSMode,
  type GroupApplicationWorkerHealth,
} from "@/api/admin/groupApplications";
import type { GroupApplicationStatus } from "@/api/groupApplications";

const { t } = useI18n();
const appStore = useAppStore();
type Tab = "applications" | "policies" | "email";
type TestingAction = "smtp" | "send" | "imap";
const activeTab = ref<Tab>("applications");
const loading = ref(false);
const saving = ref(false);
const testingAction = ref<TestingAction | null>(null);
const tabs = computed(() => [
  { value: "applications" as const, label: t("admin.groupApplications.applications") },
  { value: "policies" as const, label: t("admin.groupApplications.policies") },
  { value: "email" as const, label: t("admin.groupApplications.emailSettings") },
]);
const statuses: GroupApplicationStatus[] = [
  "pending",
  "awaiting_reply",
  "completed",
  "rejected",
  "revoked",
];
const filters = ref({ status: "" as GroupApplicationStatus | "", search: "" });
const applications = ref<AdminGroupApplication[]>([]);
const totalApplications = ref(0);
const page = ref(1);
const pageSize = 50;
const selectedApplication = ref<AdminGroupApplication | null>(null);
const communications = ref<GroupApplicationCommunication[]>([]);
const communicationsLoading = ref(false);
let applicationRequestVersion = 0;
const policies = ref<GroupApplicationPolicy[]>([]);
const groups = ref<AdminGroup[]>([]);
const selectedGroupID = ref(0);
const policyForm = ref<GroupApplicationPolicy | null>(null);
const attachment = ref<File>();
const fileInput = ref<HTMLInputElement>();
const emailForm = ref<GroupApplicationEmailConfig>();
const workerHealth = ref<GroupApplicationWorkerHealth>();
const mailboxes = ref<string[]>(["INBOX"]);
const testRecipient = ref("");
const decisionMode = ref<"reject" | "revoke" | null>(null);
const decisionReason = ref("");

const eligibleGroups = computed(() =>
  groups.value.filter(
    (group) =>
      group.is_exclusive &&
      group.subscription_type === "standard" &&
      group.status === "active",
  ),
);
const eligibleGroupOptions = computed(() =>
  eligibleGroups.value.map((group) => ({ value: group.id, label: group.name })),
);
const statusOptions = computed(() => [
  { value: "", label: t("admin.groupApplications.allStatuses") },
  ...statuses.map((status) => ({
    value: status,
    label: t(`groupApplications.status.${status}`),
  })),
]);
const tlsOptions = computed(() => [
  { value: "implicit", label: t("admin.groupApplications.implicitTLS") },
  { value: "starttls", label: "STARTTLS" },
]);
const mailboxOptions = computed(() => {
  const values = new Set(mailboxes.value);
  if (emailForm.value?.imap.mailbox) values.add(emailForm.value.imap.mailbox);
  return Array.from(values).map((value) => ({ value, label: value }));
});
const applicationHeadings = computed(() => [
  "ID",
  t("admin.groupApplications.user"),
  t("groupApplications.group"),
  t("groupApplications.contactEmail"),
  t("common.status"),
  t("common.createdAt"),
  t("admin.groupApplications.email"),
]);
const workflowStatusText = computed(() => {
  if (workerHealth.value?.configuration_error) {
    return t("admin.groupApplications.workflowConfigError");
  }
  if (!emailForm.value?.enabled) return t("admin.groupApplications.workflowPaused");
  if (workerHealth.value?.running && workerHealth.value?.workflow_enabled) {
    return t("admin.groupApplications.workflowRunning");
  }
  return t("admin.groupApplications.workerStopped");
});
const workflowStatusClass = computed(() => {
  if (workerHealth.value?.configuration_error) return "text-red-600 dark:text-red-300";
  if (!emailForm.value?.enabled) return "text-gray-500 dark:text-dark-400";
  return workerHealth.value?.workflow_enabled
    ? "text-green-600 dark:text-green-300"
    : "text-amber-600 dark:text-amber-300";
});

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
function errorMessage(error: unknown) {
  return extractI18nErrorMessage(
    error,
    t,
    "admin.groupApplications.errors",
    t("common.error"),
  );
}
async function loadApplications() {
  loading.value = true;
  try {
    const result = await groupApplicationsAdminAPI.list({
      ...filters.value,
      page: page.value,
      page_size: pageSize,
    });
    applications.value = result.items;
    totalApplications.value = result.total;
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
async function applyFilters() {
  page.value = 1;
  await loadApplications();
}
function setStatusFilter(value: string | number | boolean | null) {
  filters.value.status = (value || "") as GroupApplicationStatus | "";
  void applyFilters();
}
async function changePage(nextPage: number) {
  if (nextPage < 1 || nextPage === page.value) return;
  page.value = nextPage;
  await loadApplications();
}
async function openApplication(id: number) {
  const requestVersion = ++applicationRequestVersion;
  communicationsLoading.value = true;
  try {
    const [application, history] = await Promise.all([
      groupApplicationsAdminAPI.get(id),
      groupApplicationsAdminAPI.listCommunications(id),
    ]);
    if (requestVersion === applicationRequestVersion) {
      selectedApplication.value = application;
      communications.value = history;
    }
  } catch (error) {
    if (requestVersion === applicationRequestVersion) appStore.showError(errorMessage(error));
  } finally {
    if (requestVersion === applicationRequestVersion) communicationsLoading.value = false;
  }
}
function closeApplication() {
  applicationRequestVersion += 1;
  selectedApplication.value = null;
  communications.value = [];
  communicationsLoading.value = false;
}
async function loadPolicies() {
  loading.value = true;
  try {
    [policies.value, groups.value] = await Promise.all([
      groupApplicationsAdminAPI.listPolicies(),
      adminAPI.groups.getAll(),
    ]);
    const selectedID = selectedGroupID.value || policies.value[0]?.group_id || eligibleGroups.value[0]?.id || 0;
    selectPolicy(Number(selectedID), true);
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
function setSelectedGroup(value: string | number | boolean | null) {
  const groupID = Number(value || 0);
  if (groupID > 0) selectPolicy(groupID);
}
function selectPolicy(groupID: number, force = false) {
  if (!force && selectedGroupID.value === groupID && policyForm.value?.group_id === groupID) return;
  selectedGroupID.value = groupID;
  const existing = policies.value.find((item) => Number(item.group_id) === groupID);
  const group = eligibleGroups.value.find((item) => Number(item.id) === groupID);
  policyForm.value = existing
    ? clone(existing)
    : group
      ? {
          group_id: group.id,
          group_name: group.name,
          enabled: false,
          reply_phrase: "",
          templates: defaultGroupApplicationTemplates(),
        }
      : null;
  attachment.value = undefined;
  if (fileInput.value) fileInput.value.value = "";
}
function handleAttachment(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (file && (file.type !== "application/pdf" || file.size > 10 * 1024 * 1024)) {
    appStore.showError(t("admin.groupApplications.invalidPDF"));
    input.value = "";
    return;
  }
  attachment.value = file;
}
async function savePolicy() {
  if (!policyForm.value) return;
  saving.value = true;
  try {
    const saved = await groupApplicationsAdminAPI.savePolicy(
      policyForm.value.group_id,
      policyForm.value,
      attachment.value,
    );
    const index = policies.value.findIndex((item) => item.group_id === saved.group_id);
    if (index >= 0) policies.value[index] = saved;
    else policies.value.push(saved);
    policyForm.value = clone(saved);
    attachment.value = undefined;
    if (fileInput.value) fileInput.value.value = "";
    appStore.showSuccess(t("common.saved"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function loadEmailConfig() {
  loading.value = true;
  try {
    [emailForm.value, workerHealth.value] = await Promise.all([
      groupApplicationsAdminAPI.getEmailConfig(),
      groupApplicationsAdminAPI.workerStatus(),
    ]);
    const mailbox = emailForm.value.imap.mailbox || "INBOX";
    mailboxes.value = Array.from(new Set(["INBOX", mailbox]));
    if (!testRecipient.value) {
      testRecipient.value = emailForm.value.smtp.from_address;
    }
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
function setSMTPTLSMode(value: string | number | boolean | null) {
  if (!emailForm.value || (value !== "implicit" && value !== "starttls")) return;
  const oldMode = emailForm.value.smtp.tls_mode;
  const oldDefault = oldMode === "implicit" ? 465 : 587;
  if (!emailForm.value.smtp.port || emailForm.value.smtp.port === oldDefault) {
    emailForm.value.smtp.port = value === "implicit" ? 465 : 587;
  }
  emailForm.value.smtp.tls_mode = value;
}
function setIMAPTLSMode(value: string | number | boolean | null) {
  if (!emailForm.value || (value !== "implicit" && value !== "starttls")) return;
  const oldMode = emailForm.value.imap.tls_mode;
  const oldDefault = oldMode === "implicit" ? 993 : 143;
  if (!emailForm.value.imap.port || emailForm.value.imap.port === oldDefault) {
    emailForm.value.imap.port = value === "implicit" ? 993 : 143;
  }
  emailForm.value.imap.tls_mode = value;
}
function tlsHelp(mode: GroupApplicationTLSMode, transport: "smtp" | "imap") {
  const port = transport === "smtp" ? (mode === "implicit" ? 465 : 587) : mode === "implicit" ? 993 : 143;
  return mode === "implicit"
    ? t("admin.groupApplications.implicitTLSHint", { port })
    : t("admin.groupApplications.startTLSHint", { port });
}
function passwordHint(configured: boolean) {
  return configured
    ? t("admin.groupApplications.passwordConfiguredHint")
    : t("admin.groupApplications.passwordRequiredHint");
}
async function saveEmailConfig() {
  if (!emailForm.value) return;
  saving.value = true;
  try {
    emailForm.value = await groupApplicationsAdminAPI.saveEmailConfig(emailForm.value);
    await loadEmailConfig();
    appStore.showSuccess(t("common.saved"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function testSMTP() {
  if (!emailForm.value) return;
  testingAction.value = "smtp";
  try {
    await groupApplicationsAdminAPI.testSMTP(emailForm.value);
    appStore.showSuccess(t("admin.groupApplications.smtpConnectionOK"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    testingAction.value = null;
  }
}
async function sendTestEmail() {
  if (!emailForm.value || !testRecipient.value) return;
  testingAction.value = "send";
  try {
    await groupApplicationsAdminAPI.sendTestEmail(emailForm.value, testRecipient.value);
    appStore.showSuccess(t("admin.groupApplications.testEmailSent"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    testingAction.value = null;
  }
}
async function testIMAP() {
  if (!emailForm.value) return;
  testingAction.value = "imap";
  try {
    const result = await groupApplicationsAdminAPI.testIMAP(emailForm.value);
    mailboxes.value = Array.from(new Set(["INBOX", ...result.mailboxes]));
    appStore.showSuccess(
      t("admin.groupApplications.imapConnectionOK", { count: result.mailboxes.length }),
    );
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    testingAction.value = null;
  }
}
async function reloadActiveTab() {
  if (activeTab.value === "applications") await loadApplications();
  else if (activeTab.value === "policies") await loadPolicies();
  else await loadEmailConfig();
}
async function approveSelected() {
  if (!selectedApplication.value) return;
  saving.value = true;
  try {
    await groupApplicationsAdminAPI.approve(selectedApplication.value.id);
    await openApplication(selectedApplication.value.id);
    await loadApplications();
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
function openDecision(mode: "reject" | "revoke") {
  decisionMode.value = mode;
  decisionReason.value = "";
}
async function submitDecision() {
  if (!selectedApplication.value || !decisionMode.value || !decisionReason.value.trim()) return;
  saving.value = true;
  try {
    if (decisionMode.value === "reject") {
      await groupApplicationsAdminAPI.reject(selectedApplication.value.id, decisionReason.value);
    } else {
      await groupApplicationsAdminAPI.revoke(selectedApplication.value.id, decisionReason.value);
    }
    decisionMode.value = null;
    await openApplication(selectedApplication.value.id);
    await loadApplications();
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function resendApproval() {
  if (!selectedApplication.value) return;
  try {
    await groupApplicationsAdminAPI.resendApproval(selectedApplication.value.id);
    await openApplication(selectedApplication.value.id);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
async function retryMail(outboxID: number) {
  if (!selectedApplication.value || !emailForm.value?.enabled) return;
  try {
    await groupApplicationsAdminAPI.retryMail(selectedApplication.value.id, outboxID);
    await openApplication(selectedApplication.value.id);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
async function downloadAgreement() {
  if (!policyForm.value?.attachment_id) return;
  try {
    const blob = await groupApplicationsAdminAPI.downloadAttachment(policyForm.value.attachment_id);
    downloadBlob(blob, policyForm.value.attachment_name || "agreement.pdf");
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
async function exportCommunications() {
  if (!selectedApplication.value) return;
  try {
    const id = selectedApplication.value.id;
    const blob = await groupApplicationsAdminAPI.exportCommunications(id);
    downloadBlob(blob, `group-application-${id}-communications.json`);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  queueMicrotask(() => URL.revokeObjectURL(url));
}
function formatDate(value: string) {
  return new Date(value).toLocaleString();
}
function formatBytes(value: number) {
  return value < 1024 * 1024
    ? `${Math.max(1, Math.round(value / 1024))} KiB`
    : `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
function statusClass(status: GroupApplicationStatus) {
  return {
    pending: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300",
    awaiting_reply: "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300",
    completed: "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300",
    rejected: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300",
    revoked: "bg-gray-200 text-gray-700 dark:bg-dark-600 dark:text-dark-200",
  }[status];
}

onMounted(async () => {
  await Promise.all([loadApplications(), loadPolicies(), loadEmailConfig()]);
});
</script>
