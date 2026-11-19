/** @type {import('tailwindcss').Config} */
// Colors are CSS custom properties (see src/index.css) so light and dark
// themes swap in one place.
const token = (name) => `rgb(var(--${name}) / <alpha-value>)`;

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        surface: token("surface"),
        raised: token("raised"),
        sunken: token("sunken"),
        line: token("line"),
        ink: token("ink"),
        "ink-2": token("ink-2"),
        "ink-3": token("ink-3"),
        accent: token("accent"),
        "accent-soft": token("accent-soft"),
        good: token("good"),
        warning: token("warning"),
        critical: token("critical"),
      },
      fontFamily: {
        sans: ["Inter", "ui-sans-serif", "system-ui", "-apple-system", "Segoe UI", "Roboto", "sans-serif"],
      },
    },
  },
  plugins: [],
};
