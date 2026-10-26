import "@fontsource-variable/ibm-plex-sans";
import "@fontsource-variable/jetbrains-mono";
import "./app.css";
import { mount } from "svelte";
import App from "./App.svelte";
import { appearance } from "./lib/theme.svelte";

// Set the effective appearance before the first render.
appearance.apply();
mount(App, { target: document.getElementById("app")! });
