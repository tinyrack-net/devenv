import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";

import "./styles.css";
import { ApiError } from "./api";
import { Layout } from "./components/Layout";
import { EnvironmentsPage } from "./pages/Environments";
import { NewEnvironmentPage } from "./pages/NewEnvironment";
import { EnvironmentDetailPage } from "./pages/EnvironmentDetail";
import { ImagesPage } from "./pages/Images";
import { SSHKeysPage } from "./pages/SSHKeys";
import { LoginPage } from "./pages/Login";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    element: <Layout />,
    children: [
      { index: true, element: <EnvironmentsPage /> },
      { path: "environments/new", element: <NewEnvironmentPage /> },
      { path: "environments/:name", element: <EnvironmentDetailPage /> },
      { path: "images", element: <ImagesPage /> },
      { path: "ssh-keys", element: <SSHKeysPage /> },
    ],
  },
]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
