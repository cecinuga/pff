// Firebase initialisation from the runtime configuration served by the backend.
import { initializeApp, type FirebaseApp } from "firebase/app";
import { getAuth, type Auth } from "firebase/auth";
import type { PublicConfig } from "./types";

let app: FirebaseApp | null = null;
let auth: Auth | null = null;

export function initFirebase(cfg: PublicConfig): { app: FirebaseApp; auth: Auth } {
  if (!app) {
    app = initializeApp(cfg.firebase);
    auth = getAuth(app);
  }
  return { app, auth: auth! };
}

export function firebaseApp(): FirebaseApp | null {
  return app;
}
