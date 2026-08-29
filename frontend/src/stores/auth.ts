import { defineStore } from "pinia";
import { detectLocale, setLocale } from "@/i18n";
import { cloneDeep } from "lodash-es";

export const useAuthStore = defineStore("auth", {
  // convert to a function
  state: (): {
    user: IUser | null;
    jwt: string;
    logoutTimer: number | null;
  } => ({
    user: null,
    jwt: "",
    logoutTimer: null,
  }),
  getters: {
    // user and jwt getter removed, no longer needed
    isLoggedIn: (state) => state.user !== null,
  },
  actions: {
    // no context as first argument, use `this` instead
    setUser(user: IUser) {
      if (user === null) {
        this.user = null;
        return;
      }

      setLocale(user.locale || detectLocale());
      // Hide dotfiles and single-click open are enforced for every user,
      // regardless of what is stored in their profile.
      this.user = { ...user, hideDotfiles: true, singleClick: true };
    },
    updateUser(user: Partial<IUser>) {
      if (user.locale) {
        setLocale(user.locale);
      }

      this.user = {
        ...this.user,
        ...cloneDeep(user),
        hideDotfiles: true,
        singleClick: true,
      } as IUser;
    },
    // easily reset state using `$reset`
    clearUser() {
      this.$reset();
    },
    setLogoutTimer(logoutTimer: number | null) {
      this.logoutTimer = logoutTimer;
    },
  },
});
