import { useQuery } from "@tanstack/react-query";
import { api } from "../../lib/api";
import { usePrefs, type Theme } from "../../lib/prefs";
import { PageHeader } from "../../components/Layout";
import { Field, cx } from "../../components/ui";

export function PreferencesPage() {
  const [prefs, update] = usePrefs();
  const images = useQuery({ queryKey: ["images"], queryFn: () => api.images() });
  const classes = useQuery({ queryKey: ["classes"], queryFn: api.classes });
  return (
    <>
      <PageHeader title="Preferences" subtitle="Saved to your account and applied in every browser." />
      <div className="max-w-2xl space-y-4">
        <section className="rounded-box bg-base-100 p-5">
          <h2 className="mb-2 font-semibold">Appearance</h2>
          <Field label="Theme">
            <div className="join">
              {(["system", "light", "dark"] as Theme[]).map((t) => (
                <button key={t} className={cx("btn btn-sm join-item capitalize", prefs.theme === t && "btn-active")} onClick={() => update({ theme: t })}>
                  {t}
                </button>
              ))}
            </div>
          </Field>
        </section>
        <section className="rounded-box bg-base-100 p-5">
          <h2 className="mb-2 font-semibold">Terminal</h2>
          <div className="grid gap-x-4 sm:grid-cols-2">
            <Field label={`Font size · ${prefs.terminalFontSize}px`}>
              <input type="range" className="range range-sm" min={10} max={22} value={prefs.terminalFontSize} onChange={(e) => update({ terminalFontSize: Number(e.target.value) })} />
            </Field>
            <Field label="Colors">
              <select className="select w-full" value={prefs.terminalTheme} onChange={(e) => update({ terminalTheme: e.target.value as "dark" | "light" | "auto" })}>
                <option value="auto">Follow theme</option>
                <option value="dark">Dark</option>
                <option value="light">Light</option>
              </select>
            </Field>
            <Field label="Font family" className="sm:col-span-2">
              <input className="input font-mono-tight w-full" value={prefs.terminalFontFamily} onChange={(e) => update({ terminalFontFamily: e.target.value })} />
            </Field>
            <Field label="Scrollback lines">
              <input type="number" className="input w-full" min={1000} max={100000} step={1000} value={prefs.terminalScrollback} onChange={(e) => update({ terminalScrollback: Number(e.target.value) })} />
            </Field>
          </div>
        </section>
        <section className="rounded-box bg-base-100 p-5">
          <h2 className="mb-2 font-semibold">New environments</h2>
          <div className="grid gap-x-4 sm:grid-cols-2">
            <Field label="Default image">
              <select className="select w-full" value={prefs.defaultImage ?? ""} onChange={(e) => update({ defaultImage: e.target.value || undefined })}>
                <option value="">First available</option>
                {images.data?.map((i) => (
                  <option key={i.name} value={i.name}>
                    {i.displayName || i.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Default class">
              <select className="select w-full" value={prefs.defaultClass ?? ""} onChange={(e) => update({ defaultClass: e.target.value || undefined })}>
                <option value="">Default</option>
                {classes.data?.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </section>
      </div>
    </>
  );
}
