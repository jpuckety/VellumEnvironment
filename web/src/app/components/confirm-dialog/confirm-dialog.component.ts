import { Component, EventEmitter, Input, Output } from '@angular/core';
import { CommonModule } from '@angular/common';

@Component({
  selector: 'app-confirm-dialog',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="modal-overlay" *ngIf="isOpen" (click)="onCancel()">
      <div class="modal-content" (click)="$event.stopPropagation()">
        <div class="dialog-icon-wrapper">
          <div class="dialog-icon">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M3 6h18"/>
              <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/>
              <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>
              <line x1="10" y1="11" x2="10" y2="17"/>
              <line x1="14" y1="11" x2="14" y2="17"/>
            </svg>
          </div>
        </div>

        <h3 class="dialog-title">{{ title }}</h3>
        <p class="dialog-message">{{ message }}</p>

        <div class="dialog-actions">
          <button type="button" class="btn btn-secondary" (click)="onCancel()" [disabled]="loading">
            Cancel
          </button>
          <button type="button" class="btn btn-danger" (click)="onConfirm()" [disabled]="loading">
            <span *ngIf="loading">Deleting...</span>
            <span *ngIf="!loading">{{ confirmText }}</span>
          </button>
        </div>
      </div>
    </div>
  `,
  styles: [`
    .dialog-icon-wrapper {
      display: flex;
      justify-content: center;
      margin-bottom: 16px;
    }

    .dialog-icon {
      width: 48px;
      height: 48px;
      border-radius: 50%;
      background-color: var(--danger-light);
      color: var(--danger);
      display: flex;
      align-items: center;
      justify-content: center;
    }

    .dialog-icon svg {
      width: 24px;
      height: 24px;
    }

    .dialog-title {
      text-align: center;
      font-size: 18px;
      margin-bottom: 8px;
    }

    .dialog-message {
      text-align: center;
      font-size: 14px;
      color: var(--gray-600);
      margin-bottom: 24px;
      line-height: 1.5;
    }

    .dialog-actions {
      display: flex;
      justify-content: flex-end;
      gap: 12px;
    }

    .dialog-actions button {
      min-width: 90px;
    }
  `]
})
export class ConfirmDialogComponent {
  @Input() isOpen = false;
  @Input() title = 'Confirm Deletion';
  @Input() message = 'Are you sure you want to delete this item? This action cannot be undone.';
  @Input() confirmText = 'Delete';
  @Input() loading = false;

  @Output() confirm = new EventEmitter<void>();
  @Output() cancel = new EventEmitter<void>();

  onConfirm(): void {
    this.confirm.emit();
  }

  onCancel(): void {
    this.cancel.emit();
  }
}
