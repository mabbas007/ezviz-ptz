/*
 * Fake Hikvision SDK used by the integration test.
 *
 * The real SDK only runs against a camera, so for tests we LD_PRELOAD this
 * shared object over libhcnetsdk.so.  It accepts every command and reports a
 * fixed PTZ position so the whole ONVIF surface can be exercised offline.
 *
 * Build with: gcc -shared -fPIC -o libfakesdk.so fake_hcnetsdk.c
 */
typedef unsigned short WORD;
typedef unsigned int DWORD;
typedef int LONG;
typedef int BOOL;

int NET_DVR_Init(void) { return 1; }
int NET_DVR_Cleanup(void) { return 1; }
unsigned int NET_DVR_GetLastError(void) { return 0; }

LONG NET_DVR_Login_V30(char *ip, WORD port, char *user, char *pass, void *di) {
    (void)ip;
    (void)port;
    (void)user;
    (void)pass;
    if (di) {
        /* NET_DVR_DEVICEINFO_V30.byStartChan / byStartDChan, see HCNetSDK.h */
        ((unsigned char *)di)[564] = 1;
    }
    return 0;
}

BOOL NET_DVR_Logout(LONG uid) {
    (void)uid;
    return 1;
}

int NET_DVR_PTZControl_Other(LONG uid, LONG chan, DWORD cmd, DWORD stop) {
    (void)uid;
    (void)chan;
    (void)cmd;
    (void)stop;
    return 1;
}

int NET_DVR_PTZControlWithSpeed_Other(
    LONG uid, LONG chan, DWORD cmd, DWORD stop, DWORD speed
) {
    (void)uid;
    (void)chan;
    (void)cmd;
    (void)stop;
    (void)speed;
    return 1;
}

int NET_DVR_GetDVRConfig(
    LONG uid, DWORD cmd, LONG chan, void *buf, DWORD size, DWORD *ret
) {
    (void)uid;
    (void)cmd;
    (void)chan;
    (void)size;
    /* NET_DVR_PTZPOS: wAction, wPanPos, wTiltPos, wZoomPos */
    unsigned short *p = (unsigned short *)buf;
    p[0] = 0;
    p[1] = 1234;
    p[2] = 5678;
    p[3] = 90;
    if (ret) {
        *ret = 0;
    }
    return 1;
}
