import { Routes } from '@angular/router';
import { authGuard } from './guards/auth.guard';
import { LoginComponent } from './components/login/login.component';
import { AccountListComponent } from './components/account-list/account-list.component';
import { AccountFormComponent } from './components/account-form/account-form.component';

export const routes: Routes = [
  { path: '', component: AccountListComponent, canActivate: [authGuard] },
  { path: 'accounts/new', component: AccountFormComponent, canActivate: [authGuard] },
  { path: 'accounts/edit/:id', component: AccountFormComponent, canActivate: [authGuard] },
  { path: 'login', component: LoginComponent },
  { path: '**', redirectTo: '' }
];
