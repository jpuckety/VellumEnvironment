import { Injectable } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable, map, timeout } from 'rxjs';
import { Account, AccountSummary, ProviderPreset, VerificationResult, VerifyRequest } from '../models/account.model';

@Injectable({
  providedIn: 'root'
})
export class AccountService {
  private readonly baseUrl = '/api/accounts';

  constructor(private http: HttpClient) {}

  getAccounts(): Observable<AccountSummary[]> {
    return this.http.get<AccountSummary[] | { accounts?: AccountSummary[] | null }>(this.baseUrl).pipe(
      map((res) => {
        if (Array.isArray(res)) {
          return res;
        }
        return Array.isArray(res?.accounts) ? res.accounts : [];
      })
    );
  }

  getAccount(id: string): Observable<Account> {
    return this.http.get<Account>(`${this.baseUrl}/${encodeURIComponent(id)}`);
  }

  createAccount(account: Partial<Account>): Observable<Account> {
    return this.http.post<Account>(this.baseUrl, account);
  }

  updateAccount(id: string, account: Partial<Account>): Observable<Account> {
    return this.http.put<Account>(`${this.baseUrl}/${encodeURIComponent(id)}`, account);
  }

  deleteAccount(id: string): Observable<{ success: boolean }> {
    return this.http.delete<{ success: boolean }>(`${this.baseUrl}/${encodeURIComponent(id)}`);
  }

  verifyAccount(data: VerifyRequest): Observable<VerificationResult> {
    return this.http.post<VerificationResult>(`${this.baseUrl}/verify`, data).pipe(
      timeout({ first: 30_000 })
    );
  }

  getPresets(): ProviderPreset[] {
    return [
      {
        name: 'Gmail / Google Workspace',
        imap_host: 'imap.gmail.com',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: 'smtp.gmail.com',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Requires 2-Step Verification enabled and a 16-character App Password generated in Google Account settings.'
      },
      {
        name: 'Microsoft 365 / Outlook',
        imap_host: 'outlook.office365.com',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: 'smtp.office365.com',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Requires Authenticated SMTP and IMAP enabled in Microsoft 365 admin / app password if MFA is enforced.'
      },
      {
        name: 'Fastmail',
        imap_host: 'imap.fastmail.com',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: 'smtp.fastmail.com',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Use an App-Specific Password generated under Fastmail Settings > Passwords & Security.'
      },
      {
        name: 'Apple iCloud',
        imap_host: 'imap.mail.me.com',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: 'smtp.mail.me.com',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Generate an App-Specific Password at appleid.apple.com.'
      },
      {
        name: 'Yahoo Mail',
        imap_host: 'imap.mail.yahoo.com',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: 'smtp.mail.yahoo.com',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Generate an App Password in Yahoo Account Security settings.'
      },
      {
        name: 'Custom IMAP / SMTP',
        imap_host: '',
        imap_port: 993,
        imap_use_tls: true,
        smtp_host: '',
        smtp_port: 587,
        smtp_use_tls: true,
        instructions: 'Enter your custom mail server hostnames and ports. TLS encryption is required.'
      }
    ];
  }
}
