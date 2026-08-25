import { Component, inject } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router, RouterLink } from '@angular/router';
import { AuthService } from '../../services/auth.service';

@Component({
  selector: 'app-header',
  standalone: true,
  imports: [CommonModule, RouterLink],
  template: `
    <header class="header">
      <div class="header-container">
        <a routerLink="/" class="brand">
          <svg class="brand-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect width="20" height="16" x="2" y="4" rx="2"/>
            <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/>
          </svg>
          <span class="brand-title">EmailMCP</span>
          <span class="brand-badge">Accounts</span>
        </a>

        <div class="user-menu" *ngIf="authService.isAuthenticated()">
          <div class="user-info">
            <span class="user-icon">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/>
                <circle cx="12" cy="7" r="4"/>
              </svg>
            </span>
            <span class="user-email" [title]="authService.user()?.email">{{ authService.user()?.email }}</span>
          </div>
          <button (click)="logout()" class="btn btn-secondary btn-sm" title="Sign out of EmailMCP">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="14" height="14">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>
              <polyline points="16 17 21 12 16 7"/>
              <line x1="21" x2="9" y1="12" y2="12"/>
            </svg>
            Sign Out
          </button>
        </div>
      </div>
    </header>
  `,
  styles: [`
    .header {
      background: white;
      border-bottom: 1px solid var(--gray-200);
      box-shadow: var(--shadow-sm);
      position: sticky;
      top: 0;
      z-index: 30;
    }

    .header-container {
      max-width: 1024px;
      margin: 0 auto;
      padding: 12px 16px;
      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    .brand {
      display: flex;
      align-items: center;
      gap: 10px;
      text-decoration: none;
      color: inherit;
    }

    .brand-icon {
      width: 28px;
      height: 28px;
      color: var(--primary);
    }

    .brand-title {
      font-size: 18px;
      font-weight: 700;
      color: var(--gray-900);
      letter-spacing: -0.02em;
    }

    .brand-badge {
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      background-color: var(--primary-light);
      color: var(--primary);
      padding: 2px 6px;
      border-radius: var(--radius-sm);
      letter-spacing: 0.05em;
    }

    .user-menu {
      display: flex;
      align-items: center;
      gap: 14px;
    }

    .user-info {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 13px;
      color: var(--gray-700);
      background-color: var(--gray-100);
      padding: 4px 10px;
      border-radius: 9999px;
      border: 1px solid var(--gray-200);
    }

    .user-icon {
      display: flex;
      align-items: center;
      width: 14px;
      height: 14px;
      color: var(--gray-500);
    }

    .user-email {
      font-weight: 500;
      max-width: 200px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .btn-sm {
      padding: 4px 10px;
      font-size: 13px;
    }

    @media (max-width: 600px) {
      .user-email {
        max-width: 120px;
      }
    }
  `]
})
export class HeaderComponent {
  authService = inject(AuthService);
  private router = inject(Router);

  logout(): void {
    this.authService.logout().subscribe(() => {
      this.router.navigate(['/login']);
    });
  }
}
