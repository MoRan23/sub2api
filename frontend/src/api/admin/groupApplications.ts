import { apiClient } from "../client";
import type {
  GroupApplication,
  GroupApplicationStatus,
} from "../groupApplications";

export type GroupApplicationMailKind =
  | "approval"
  | "completion"
  | "manual_rejection"
  | "reply_mismatch"
  | "revocation";

export interface LocalizedMailTemplate {
  subject: string;
  html: string;
}
export type GroupApplicationTemplateSet = Record<
  GroupApplicationMailKind,
  Record<"zh" | "en", LocalizedMailTemplate>
>;

export interface GroupApplicationPolicy {
  group_id: number;
  group_name: string;
  enabled: boolean;
  reply_phrase: string;
  templates: GroupApplicationTemplateSet;
  attachment_id?: number;
  attachment_name?: string;
  attachment_size?: number;
  attachment_sha256?: string;
  created_at?: string;
  updated_at?: string;
}

export interface GroupApplicationMailStatus {
  id: number;
  kind: GroupApplicationMailKind;
  message_id: string;
  status: string;
  attempts: number;
  last_error?: string;
  sent_at?: string;
  created_at: string;
}

export interface GroupApplicationCommunication {
  id: number;
  application_id: number;
  direction: "outbound" | "inbound";
  kind?: GroupApplicationMailKind;
  result?: string;
  from_address?: string;
  to_address?: string;
  subject?: string;
  html_body?: string;
  text_body?: string;
  content_unavailable?: boolean;
  content_truncated?: boolean;
  message_id?: string;
  in_reply_to?: string;
  references?: string;
  reply_sha256?: string;
  attachment_id?: number;
  attachment_name?: string;
  attachment_size?: number;
  status?: string;
  attempts?: number;
  last_error?: string;
  sent_at?: string;
  occurred_at: string;
}

export interface AdminGroupApplication extends GroupApplication {
  mails?: GroupApplicationMailStatus[];
}
export interface GroupApplicationListResult {
  items: AdminGroupApplication[];
  total: number;
}

export interface GroupApplicationIMAPConfig {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  password?: string;
  password_configured: boolean;
  mailbox: string;
  reply_address: string;
  tls_mode: "implicit" | "starttls";
  poll_interval_seconds: number;
}

export interface GroupApplicationWorkerHealth {
  running: boolean;
  mail_processed: number;
  mail_failures: number;
  replies_processed: number;
  reply_failures: number;
  last_imap_check_at?: string;
  last_imap_error?: string;
}

export function defaultGroupApplicationTemplates(): GroupApplicationTemplateSet {
  return {
    approval: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 分组申请已批准",
        html: "<p>您的申请已被批准，但在正式为您开放访问权限前，您需仔细阅读附件中的协议，并直接回复本邮件“<strong>{{reply_phrase}}</strong>”。回复正文不得包含其他内容或签名。</p>",
      },
      en: {
        subject: "{{site_name}} - {{group_name}} application approved",
        html: "<p>Your application has been approved. Before access is opened, read the attached agreement and reply to this email with exactly <strong>{{reply_phrase}}</strong>. Do not include any other text or signature.</p>",
      },
    },
    completion: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 访问权限已开放",
        html: "<p>您对 {{group_name}} 的申请已完成，访问权限现已开放。请前往 API 密钥页将需要使用的密钥切换到该分组。</p>",
      },
      en: {
        subject: "{{site_name}} - {{group_name}} access enabled",
        html: "<p>Your application for {{group_name}} is complete and access is now enabled. Open the API Keys page to switch the keys that should use this group.</p>",
      },
    },
    manual_rejection: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 分组申请未通过",
        html: "<p>您的 {{group_name}} 分组申请未通过。拒绝理由：{{decision_reason}}。</p>",
      },
      en: {
        subject: "{{site_name}} - {{group_name}} application declined",
        html: "<p>Your application for {{group_name}} was declined. Reason: {{decision_reason}}.</p>",
      },
    },
    reply_mismatch: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 邮件确认不匹配",
        html: "<p>您的邮件回复与要求的确认内容不完全一致，本次申请已自动拒绝。您可以重新提交申请。</p>",
      },
      en: {
        subject: "{{site_name}} - {{group_name}} confirmation did not match",
        html: "<p>Your reply did not exactly match the required confirmation. This application was automatically declined and you may submit a new application.</p>",
      },
    },
    revocation: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 访问权限已撤销",
        html: "<p>您的 {{group_name}} 分组访问权限已被撤销。撤销理由：{{decision_reason}}。已绑定该分组的 API 密钥将无法继续使用。</p>",
      },
      en: {
        subject: "{{site_name}} - {{group_name}} access revoked",
        html: "<p>Your access to {{group_name}} was revoked. Reason: {{decision_reason}}. API keys already bound to this group can no longer be used.</p>",
      },
    },
  };
}

export const groupApplicationsAdminAPI = {
  async list(
    params: {
      page?: number;
      page_size?: number;
      status?: GroupApplicationStatus | "";
      search?: string;
    } = {},
  ): Promise<GroupApplicationListResult> {
    const { data } = await apiClient.get<GroupApplicationListResult>(
      "/admin/group-applications",
      { params },
    );
    return data;
  },
  async get(id: number): Promise<AdminGroupApplication> {
    const { data } = await apiClient.get<AdminGroupApplication>(
      `/admin/group-applications/${id}`,
    );
    return data;
  },
  async listCommunications(id: number): Promise<GroupApplicationCommunication[]> {
    const { data } = await apiClient.get<GroupApplicationCommunication[]>(
      `/admin/group-applications/${id}/communications`,
    );
    return data;
  },
  async exportCommunications(id: number): Promise<Blob> {
    const { data } = await apiClient.get<Blob>(
      `/admin/group-applications/${id}/communications/export`,
      { responseType: "blob" },
    );
    return data;
  },
  async approve(id: number): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/approve`,
    );
    return data;
  },
  async reject(id: number, reason: string): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/reject`,
      { reason },
    );
    return data;
  },
  async revoke(id: number, reason: string): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/revoke`,
      { reason },
    );
    return data;
  },
  async resendApproval(id: number): Promise<void> {
    await apiClient.post(`/admin/group-applications/${id}/resend-approval`);
  },
  async retryMail(id: number, outboxID: number): Promise<void> {
    await apiClient.post(
      `/admin/group-applications/${id}/mails/${outboxID}/retry`,
    );
  },
  async listPolicies(): Promise<GroupApplicationPolicy[]> {
    const { data } = await apiClient.get<GroupApplicationPolicy[]>(
      "/admin/group-application-policies",
    );
    return data;
  },
  async savePolicy(
    groupID: number,
    policy: GroupApplicationPolicy,
    attachment?: File,
  ): Promise<GroupApplicationPolicy> {
    if (attachment) {
      const form = new FormData();
      form.append("policy", JSON.stringify(policy));
      form.append("attachment", attachment);
      const { data } = await apiClient.put<GroupApplicationPolicy>(
        `/admin/group-application-policies/${groupID}`,
        form,
      );
      return data;
    }
    const { data } = await apiClient.put<GroupApplicationPolicy>(
      `/admin/group-application-policies/${groupID}`,
      policy,
    );
    return data;
  },
  async downloadAttachment(id: number): Promise<Blob> {
    const { data } = await apiClient.get<Blob>(
      `/admin/group-application-policies/attachments/${id}`,
      { responseType: "blob" },
    );
    return data;
  },
  async getIMAP(): Promise<GroupApplicationIMAPConfig> {
    const { data } = await apiClient.get<GroupApplicationIMAPConfig>(
      "/admin/group-applications/imap",
    );
    return data;
  },
  async saveIMAP(
    input: GroupApplicationIMAPConfig,
  ): Promise<GroupApplicationIMAPConfig> {
    const { data } = await apiClient.put<GroupApplicationIMAPConfig>(
      "/admin/group-applications/imap",
      input,
    );
    return data;
  },
  async testIMAP(): Promise<void> {
    await apiClient.post("/admin/group-applications/imap/test");
  },
  async workerStatus(): Promise<GroupApplicationWorkerHealth> {
    const { data } = await apiClient.get<GroupApplicationWorkerHealth>(
      "/admin/group-applications/worker-status",
    );
    return data;
  },
};

export default groupApplicationsAdminAPI;
