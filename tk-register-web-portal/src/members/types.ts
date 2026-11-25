export type Member = {
  id: string;
  email: string;
  status: string;
  created_at: string;
  visible_accounts: number;
  membership_version: string;
};
export type MemberPage = { items: Member[]; total: number; page: number; page_size: number };
