import { useEffect } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./lib/auth";
import { MetaProvider } from "./lib/meta";
import { registerServiceWorker } from "./lib/push";
import { Layout } from "./components/Layout";
import { ErrorBox, Spinner } from "./components/ui";
import { LoginPage } from "./pages/Login";
import { EmailMfaGate, EnrollmentGate, NotWhitelisted } from "./pages/Gates";
import { DashboardPage } from "./pages/Dashboard";
import { DomainPage } from "./pages/DomainPage";
import { BudgetsPage } from "./pages/Budgets";
import { ConnectionCallbackPage, ConnectionsPage } from "./pages/Connections";
import { NotificationsPage } from "./pages/Notifications";
import { SettingsPage } from "./pages/Settings";

function Gate() {
  const { status, error, config, refreshMe } = useAuth();

  useEffect(() => {
    if (config) void registerServiceWorker(config);
  }, [config]);

  switch (status) {
    case "loading":
    case "checking":
      return <div className="flex min-h-screen items-center justify-center"><Spinner /></div>;
    case "signed_out":
      return <LoginPage />;
    case "not_whitelisted":
      return <NotWhitelisted />;
    case "mfa_required":
      return <EmailMfaGate />;
    case "mfa_enrollment_required":
      return <EnrollmentGate />;
    case "error":
      return (
        <div className="mx-auto max-w-md p-6">
          <ErrorBox message={error ?? "Something went wrong"} onRetry={() => void refreshMe()} />
        </div>
      );
    case "ready":
      return (
        <MetaProvider>
          <Routes>
            <Route element={<Layout />}>
              <Route index element={<DashboardPage />} />
              <Route path="domains/:domain" element={<DomainPage />} />
              <Route path="budgets" element={<BudgetsPage />} />
              <Route path="connections" element={<ConnectionsPage />} />
              <Route path="connections/callback" element={<ConnectionCallbackPage />} />
              <Route path="notifications" element={<NotificationsPage />} />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </MetaProvider>
      );
  }
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Gate />
      </AuthProvider>
    </BrowserRouter>
  );
}
