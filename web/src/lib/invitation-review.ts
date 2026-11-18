export type InvitationOrganization = {
  memberId: string;
  memberName: string;
  memberAdmin: boolean;
};
export type InviteInfo = {
  email: string;
  memberId: string;
  memberName: string;
  memberAdmin?: boolean;
  expiresAt: string;
  accountExists: boolean;
  organizations?: InvitationOrganization[];
  organizationRevision?: string;
};

export function checkedInviteInfo(value: InviteInfo): InviteInfo {
  if (!value || typeof value.email !== "string" ||
    typeof value.memberId !== "string" || typeof value.memberName !== "string" ||
    typeof value.expiresAt !== "string" || typeof value.accountExists !== "boolean")
    throw new Error("邀请资料不完整，请重新读取邀请。");
  if (value.organizations !== undefined) {
    if (!Array.isArray(value.organizations) || !value.organizations.length ||
      value.organizations.length > 32 ||
      value.organizations.some(org => !org || typeof org.memberId !== "string" ||
        typeof org.memberName !== "string" || typeof org.memberAdmin !== "boolean") ||
      new Set(value.organizations.map(org => org.memberId)).size !== value.organizations.length ||
      value.organizations[0].memberId !== value.memberId ||
      typeof value.organizationRevision !== "string" ||
      !/^[a-f0-9]{64}$/.test(value.organizationRevision))
      throw new Error("邀请的组织清单或审核版本不完整，请重新读取邀请。");
  } else if (value.organizationRevision !== undefined &&
    (typeof value.organizationRevision !== "string" || !/^[a-f0-9]{64}$/.test(value.organizationRevision))) {
    throw new Error("邀请的组织审核版本无效，请重新读取邀请。");
  }
  return value;
}

export function invitationConsent(info: InviteInfo): { organizationRevision?: string } {
  checkedInviteInfo(info);
  return info.organizationRevision ? { organizationRevision: info.organizationRevision } : {};
}
