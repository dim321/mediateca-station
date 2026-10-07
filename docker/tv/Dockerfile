# Android TV 9 (API 28, x86) emulator for local playback checks.
# The system image is about 450MB compressed. KVM is required at run time.
FROM eclipse-temurin:17-jdk-jammy

ENV DEBIAN_FRONTEND=noninteractive \
    ANDROID_SDK_ROOT=/opt/android \
    ANDROID_HOME=/opt/android \
    ANDROID_AVD_HOME=/root/.android/avd

ENV PATH="${PATH}:${ANDROID_SDK_ROOT}/cmdline-tools/tools/bin:${ANDROID_SDK_ROOT}/platform-tools:${ANDROID_SDK_ROOT}/emulator"

RUN apt-get update \
    && apt-get install --no-install-recommends -y \
      ca-certificates wget unzip \
      libnss3 libx11-6 libxcomposite1 libxcursor1 libxi6 libxtst6 \
      libpulse0 libasound2 libgl1 libdbus-1-3 \
      libxkbcommon0 libgbm1 libxdamage1 libxfixes3 libxrandr2 \
      libdrm2 libxshmfence1 libvulkan1 \
      socat iproute2 procps \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p "${ANDROID_SDK_ROOT}/cmdline-tools" /root/.android \
    && wget -q -O /tmp/cmdtools.zip "https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip" \
    && unzip -q /tmp/cmdtools.zip -d "${ANDROID_SDK_ROOT}/cmdline-tools" \
    && mv "${ANDROID_SDK_ROOT}/cmdline-tools/cmdline-tools" "${ANDROID_SDK_ROOT}/cmdline-tools/tools" \
    && rm /tmp/cmdtools.zip \
    && touch /root/.android/repositories.cfg \
    && yes | sdkmanager --licenses \
    && sdkmanager --install \
      "system-images;android-28;android-tv;x86" \
      "platforms;android-28" \
      "platform-tools" \
      "emulator"

RUN echo no | avdmanager create avd --force \
      --name tv \
      --package "system-images;android-28;android-tv;x86" \
      --device tv_1080p

RUN wget -q -O /opt/vlc.apk "https://get.videolan.org/vlc-android/3.6.5/VLC-Android-3.6.5-x86.apk"

COPY entrypoint.sh /usr/local/bin/tv-entrypoint.sh
RUN chmod 0755 /usr/local/bin/tv-entrypoint.sh

EXPOSE 5555
ENTRYPOINT ["/usr/local/bin/tv-entrypoint.sh"]
