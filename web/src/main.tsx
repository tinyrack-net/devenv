import { StrictMode, lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router";

import "./styles.css";
import { ApiError } from "./lib/api";
import { AuthShell, Layout } from "./components/Layout";
import { Loading } from "./components/ui";
import { ToastProvider } from "./components/toast";
import { LoginPage } from "./pages/Login";
import { EnvironmentsPage } from "./pages/Environments";
import { NewEnvironmentPage } from "./pages/NewEnvironment";
import { EnvironmentSettingsPage } from "./pages/EnvironmentSettings";
import { ImagesPage } from "./pages/Images";
import { ProfilePage } from "./pages/settings/Profile";
import { SSHKeysPage } from "./pages/settings/SSHKeys";
import { TokensPage } from "./pages/settings/Tokens";
import { PreferencesPage } from "./pages/settings/Preferences";
import { AdminUsersPage } from "./pages/admin/Users";
import { AdminEnvironmentsPage } from "./pages/admin/Environments";
import { AdminClassesPage } from "./pages/admin/Classes";
import { AdminTokensPage } from "./pages/admin/Tokens";
import { AdminActivityPage } from "./pages/admin/Activity";
import { AdminSystemPage } from "./pages/admin/System";
import { NotFound } from "./pages/NotFound";

// The workspace pulls in xterm and noVNC; load it on demand.
const Workspace = lazy(() => import("./workspace/Workspace").then((m) => ({ default: m.WorkspacePage })));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      refetchOnWindowFocus: true,
      staleTime: 5_000,
    },
  },
});

const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    element: <AuthShell />,
    children: [
      {
        path: "/env/:name",
        element: (
          <Suspense fallback={<Loading label="Opening workspace…" />}>
            <Workspace />
          </Suspense>
        ),
      },
      {
        element: <Layout />,
        children: [
          { index: true, element: <EnvironmentsPage /> },
          { path: "new", element: <NewEnvironmentPage /> },
          { path: "env/:name/settings", element: <EnvironmentSettingsPage /> },
          { path: "images", element: <ImagesPage /> },
          { path: "settings", element: <Navigate to="/settings/profile" replace /> },
          { path: "settings/profile", element: <ProfilePage /> },
          { path: "settings/keys", element: <SSHKeysPage /> },
          { path: "settings/tokens", element: <TokensPage /> },
          { path: "settings/preferences", element: <PreferencesPage /> },
          { path: "admin/users", element: <AdminUsersPage /> },
          { path: "admin/environments", element: <AdminEnvironmentsPage /> },
          { path: "admin/classes", element: <AdminClassesPage /> },
          { path: "admin/tokens", element: <AdminTokensPage /> },
          { path: "admin/activity", element: <AdminActivityPage /> },
          { path: "admin/system", element: <AdminSystemPage /> },
          { path: "*", element: <NotFound /> },
        ],
      },
    ],
  },
]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
