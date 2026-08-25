export interface Account {
  id: string;
  name: string;
  imap_host: string;
  imap_port: number;
  imap_username: string;
  imap_password?: string;
  imap_use_tls: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_username?: string;
  smtp_password?: string;
  smtp_use_tls: boolean;
  from_address?: string;
  has_password?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface AccountSummary {
  id: string;
  name: string;
  imap_host: string;
  imap_port: number;
  imap_username: string;
  imap_use_tls: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_username?: string;
  smtp_use_tls: boolean;
  from_address?: string;
  has_password?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface UserInfo {
  subject: string;
  email: string;
  authenticated: boolean;
}

export interface ComponentVerification {
  success: boolean;
  message?: string;
  error?: string;
}

export interface VerificationResult {
  success: boolean;
  imap: ComponentVerification;
  smtp: ComponentVerification;
}

export interface VerifyRequest {
  id?: string;
  name?: string;
  imap_host: string;
  imap_port: number;
  imap_username: string;
  imap_password?: string;
  imap_use_tls: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_username?: string;
  smtp_password?: string;
  smtp_use_tls: boolean;
}

export interface ProviderPreset {
  name: string;
  icon?: string;
  imap_host: string;
  imap_port: number;
  imap_use_tls: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_use_tls: boolean;
  instructions?: string;
}
