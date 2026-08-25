import { Component } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterOutlet } from '@angular/router';
import { HeaderComponent } from './components/header/header.component';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [CommonModule, RouterOutlet, HeaderComponent],
  template: `
    <div class="app-layout">
      <app-header></app-header>
      <main class="main-content">
        <router-outlet></router-outlet>
      </main>
      <footer class="app-footer">
        <div class="footer-container">
          <span>EmailMCP Account Manager &bull; Secure Email Authentication for MCP</span>
          <span class="copyright">&copy; 2026 Pitaya Group, LLC</span>
        </div>
      </footer>
    </div>
  `,
  styles: [`
    .app-layout {
      min-height: 100vh;
      display: flex;
      flex-direction: column;
    }

    .main-content {
      flex: 1;
      padding-bottom: 40px;
    }

    .app-footer {
      background: white;
      border-top: 1px solid var(--gray-200);
      padding: 16px;
      text-align: center;
      font-size: 12px;
      color: var(--gray-500);
    }

    .footer-container {
      max-width: 1024px;
      margin: 0 auto;
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .copyright {
      color: var(--gray-400);
    }
  `]
})
export class AppComponent {
  title = 'EmailMCP';
}
