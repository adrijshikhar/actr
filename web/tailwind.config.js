/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ["class"],
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    container: {
      center: true,
      padding: "2rem",
      screens: {
        "2xl": "1400px",
      },
    },
    extend: {
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        geist: {
          canvas: "#000000",
          surface: "#0a0a0a",
          elevated: "#111111",
          recessed: "#000000",
          hover: "#1a1a1a",
          hairline: "#303030",
          hairlineSoft: "#262626",
          border: "#303030",
          borderHover: "#4a4a4a",
          heading: "#ffffff",
          ink: "#ededed",
          secondary: "#a1a1a1",
          body: "#a1a1a1",
          mute: "#8c8c8c",
          faint: "#8c8c8c",
          blue: "#0070f3",
          blueText: "#52a8ff",
          mint: "#30a46c",
          cyan: "#50e3c2",
          warning: "#f5a623",
          error: "#e5484d",
        },
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        popover: {
          DEFAULT: "hsl(var(--popover))",
          foreground: "hsl(var(--popover-foreground))",
        },
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
        ds: {
          red: {
            bg: "var(--ds-red-bg)",
            border: "var(--ds-red-border)",
            solid: "var(--ds-red-solid)",
            text: "var(--ds-red-text)",
          },
          amber: {
            bg: "var(--ds-amber-bg)",
            border: "var(--ds-amber-border)",
            solid: "var(--ds-amber-solid)",
            text: "var(--ds-amber-text)",
          },
          green: {
            bg: "var(--ds-green-bg)",
            border: "var(--ds-green-border)",
            solid: "var(--ds-green-solid)",
            text: "var(--ds-green-text)",
          },
          blue: {
            bg: "var(--ds-blue-bg)",
            border: "var(--ds-blue-border)",
            solid: "var(--ds-blue-solid)",
            text: "var(--ds-blue-text)",
          },
        },
        status: {
          danger: {
            bg: "var(--ds-red-bg)",
            border: "var(--ds-red-border)",
            solid: "var(--ds-red-solid)",
            text: "var(--ds-red-text)",
          },
          warning: {
            bg: "var(--ds-amber-bg)",
            border: "var(--ds-amber-border)",
            solid: "var(--ds-amber-solid)",
            text: "var(--ds-amber-text)",
          },
          success: {
            bg: "var(--ds-green-bg)",
            border: "var(--ds-green-border)",
            solid: "var(--ds-green-solid)",
            text: "var(--ds-green-text)",
          },
          info: {
            bg: "var(--ds-blue-bg)",
            border: "var(--ds-blue-border)",
            solid: "var(--ds-blue-solid)",
            text: "var(--ds-blue-text)",
          },
          meter: "var(--ds-meter-healthy)",
        },
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
        geist: "6px",
      },
      fontFamily: {
        sans: [
          "Geist",
          "-apple-system",
          "BlinkMacSystemFont",
          '"Segoe UI"',
          "Roboto",
          "sans-serif",
        ],
        mono: [
          '"Geist Mono"',
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "monospace",
        ],
      },
    },
  },
  plugins: [require("tailwindcss-animate")],
};
