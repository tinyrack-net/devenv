import { Link } from "react-router";

export function NotFound() {
  return (
    <div className="flex flex-col items-center gap-3 py-20">
      <div className="text-5xl font-bold text-base-content/30">404</div>
      <p>This page does not exist.</p>
      <Link to="/" className="btn btn-primary btn-sm">
        Back to environments
      </Link>
    </div>
  );
}
