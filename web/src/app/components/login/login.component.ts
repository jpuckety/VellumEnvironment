import { Component, inject, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { AuthService } from '../../services/auth.service';

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="login-container">
      <div class="login-card">
        <div class="login-header">
          <div class="logo-circle">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <rect width="20" height="16" x="2" y="4" rx="2"/>
              <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/>
            </svg>
          </div>
          <h1>EmailMCP Account Manager</h1>
          <p class="subtitle">
            Securely configure and manage your email accounts and passwords without exposing credentials to LLMs.
          </p>
        </div>

        <div *ngIf="errorMessage" class="alert alert-danger">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18">
            <circle cx="12" cy="12" r="10"/>
            <line x1="12" y1="8" x2="12" y2="12"/>
            <line x1="12" y1="16" x2="12.01" y2="16"/>
          </svg>
          <span>{{ errorMessage }}</span>
        </div>

        <div class="auth-actions">
          <button (click)="loginGoogle()" class="btn btn-google" [disabled]="loading">
            <svg viewBox="0 0 24 24" width="18" height="18">
              <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"/>
              <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
              <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.06H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.94l2.85-2.22.81-.63z"/>
              <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.06l3.66 2.84c.87-2.6 3.3-4.52 6.16-4.52z"/>
            </svg>
            Sign in with Google
          </button>

          <div class="divider">
            <span>or use access token</span>
          </div>

          <form (ngSubmit)="loginWithToken()" class="token-form">
            <div class="form-group">
              <label class="form-label" for="tokenInput">Bearer Access Token</label>
              <input
                id="tokenInput"
                type="password"
                class="form-input"
                [(ngModel)]="tokenInput"
                name="token"
                placeholder="Enter existing MCP session or bearer token"
                autocomplete="off"
              />
            </div>
            <button
              type="submit"
              class="btn btn-secondary w-full"
              [disabled]="!tokenInput.trim() || loading"
            >
              <span *ngIf="loading">Validating...</span>
              <span *ngIf="!loading">Authenticate with Token</span>
            </button>
          </form>
        </div>

        <div class="security-features">
          <div class="feature-item">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
              <rect width="18" height="11" x="3" y="11" rx="2" ry="2"/>
              <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
            </svg>
            <span>Passwords stored encrypted per user account</span>
          </div>
          <div class="feature-item">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
              <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
            </svg>
            <span>LLMs never see raw credentials</span>
          </div>
          <div class="feature-item">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
              <circle cx="12" cy="12" r="10"/>
              <polyline points="12 6 12 12 14 14"/>
            </svg>
            <span>Instant live IMAP/SMTP connection verification</span>
          </div>
        </div>
      </div>
    </div>
  `,
  styles: [`
    .login-container {
      min-height: calc(100vh - 120px);
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 32px 16px;
    }

    .login-card {
      background: white;
      border-radius: var(--radius-lg);
      border: 1px solid var(--gray-200);
      box-shadow: var(--shadow-lg);
      max-width: 460px;
      width: 100%;
      padding: 36px 32px;
    }

    .login-header {
      text-align: center;
      margin-bottom: 28px;
    }

    .logo-circle {
      width: 56px;
      height: 56px;
      border-radius: 16px;
      background: var(--primary-light);
      color: var(--primary);
      display: flex;
      align-items: center;
      justify-content: center;
      margin: 0 auto 16px;
    }

    .logo-circle svg {
      width: 30px;
      height: 30px;
    }

    h1 {
      font-size: 22px;
      margin-bottom: 8px;
    }

    .subtitle {
      font-size: 14px;
      color: var(--gray-500);
      line-height: 1.4;
    }

    .auth-actions {
      margin-bottom: 28px;
    }

    .btn-google {
      width: 100%;
      background: white;
      color: var(--gray-700);
      border: 1px solid var(--gray-300);
      box-shadow: var(--shadow-sm);
      padding: 10px 16px;
      font-size: 15px;
      font-weight: 500;
    }

    .btn-google:hover:not(:disabled) {
      background: var(--gray-50);
      border-color: var(--gray-400);
    }

    .divider {
      display: flex;
      align-items: center;
      text-align: center;
      margin: 20px 0;
      color: var(--gray-400);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }

    .divider::before, .divider::after {
      content: '';
      flex: 1;
      border-bottom: 1px solid var(--gray-200);
    }

    .divider span {
      padding: 0 10px;
    }

    .token-form .w-full {
      width: 100%;
    }

    .security-features {
      border-top: 1px solid var(--gray-100);
      padding-top: 20px;
      display: flex;
      flex-direction: column;
      gap: 10px;
    }

    .feature-item {
      display: flex;
      align-items: center;
      gap: 10px;
      font-size: 13px;
      color: var(--gray-600);
    }

    .feature-item svg {
      color: var(--primary);
      flex-shrink: 0;
    }
  `]
})
export class LoginComponent implements OnInit {
  private authService = inject(AuthService);
  private router = inject(Router);

  tokenInput = '';
  loading = false;
  errorMessage = '';

  ngOnInit(): void {
    if (this.authService.isAuthenticated()) {
      this.router.navigate(['/']);
    }
  }

  loginGoogle(): void {
    this.authService.loginWithGoogle();
  }

  loginWithToken(): void {
    if (!this.tokenInput.trim()) return;

    this.loading = true;
    this.errorMessage = '';

    this.authService.loginWithToken(this.tokenInput.trim()).subscribe({
      next: (authenticated) => {
        this.loading = false;
        if (authenticated) {
          this.router.navigate(['/']);
        } else {
          this.errorMessage = 'Invalid access token. Please verify and try again.';
        }
      },
      error: () => {
        this.loading = false;
        this.errorMessage = 'Authentication failed. Please check your token.';
      }
    });
  }
}
