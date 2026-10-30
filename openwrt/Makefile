# SPDX-License-Identifier: MIT
include $(TOPDIR)/rules.mk

PKG_NAME:=daed-router
PKG_VERSION:=$(if $(DAED_VERSION),$(DAED_VERSION),0.6.60)
PKG_RELEASE:=1
PKG_LICENSE:=MIT AGPL-3.0-only
PKG_MAINTAINER:=slobys
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/daed-router
  SECTION:=net
  CATEGORY:=Network
  TITLE:=NaiLink router runtime
  URL:=https://github.com/slobys/NaiLink
  DEPENDS:=+ca-bundle +jshn +jsonfilter +uclient-fetch +kmod-sched-core +kmod-sched-bpf +kmod-veth +v2ray-geoip +v2ray-geosite
  CONFLICTS:=daed
  PKGARCH:=$(if $(DAED_PKGARCH),$(DAED_PKGARCH),$(ARCH_PACKAGES))
endef

define Package/luci-app-daed-router
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI support for NaiLink
  DEPENDS:=+daed-router +luci-base +rpcd
  CONFLICTS:=luci-app-daed
  PKGARCH:=all
endef

define Package/daed-router/conffiles
/etc/config/daed
endef

define Build/Prepare
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	test -n "$(DAED_BINARY)" && test -f "$(DAED_BINARY)"
	$(CP) "$(DAED_BINARY)" $(PKG_BUILD_DIR)/daed
	test -n "$(DAED_BTF_PROBE)" && test -f "$(DAED_BTF_PROBE)"
	$(CP) "$(DAED_BTF_PROBE)" $(PKG_BUILD_DIR)/btf-probe
	test -n "$(DAED_LUCI_PROXY)" && test -f "$(DAED_LUCI_PROXY)"
	$(CP) "$(DAED_LUCI_PROXY)" $(PKG_BUILD_DIR)/luci-proxy
endef

define Build/Configure
endef
define Build/Compile
endef

define Package/daed-router/install
	$(INSTALL_DIR) $(1)/usr/libexec/daed
	$(INSTALL_BIN) ./network-check.sh $(1)/usr/libexec/daed/network-check
	$(INSTALL_DIR) $(1)/usr/bin $(1)/etc/init.d $(1)/etc/config
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/daed $(1)/usr/bin/daed
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) ./install.sh $(1)/usr/sbin/daed-router-setup
	$(INSTALL_BIN) ./files/nailink $(1)/usr/sbin/nailink
	$(INSTALL_BIN) ./files/daed.init $(1)/etc/init.d/daed
	$(INSTALL_CONF) ./files/daed.config $(1)/etc/config/daed
	$(INSTALL_DIR) $(1)/usr/libexec/daed $(1)/usr/share/daed-router
	# Keep the one-shot task outside init.d: package upgrades must not stop it.
	$(INSTALL_BIN) ./files/maintenance.init $(1)/usr/libexec/daed/nailink-maintenance
	$(INSTALL_BIN) ./files/daed-check $(1)/usr/libexec/daed/check
	$(INSTALL_BIN) ./files/subscription-proxy $(1)/usr/libexec/daed/subscription-proxy
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/btf-probe $(1)/usr/libexec/daed/btf-probe
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/luci-proxy $(1)/usr/libexec/daed/luci-proxy
	# dae resolves assets under its application name; OpenWrt owns them in v2ray.
	$(INSTALL_DIR) $(1)/usr/share/daed
	$(LN) ../v2ray/geosite.dat $(1)/usr/share/daed/geosite.dat
	$(LN) ../v2ray/geoip.dat $(1)/usr/share/daed/geoip.dat
	echo '$(PKG_VERSION)-$(PKG_RELEASE)' > $(1)/usr/share/daed-router/version
	$(INSTALL_DIR) $(1)/lib/upgrade/keep.d
	$(INSTALL_DATA) ./files/keep-daed $(1)/lib/upgrade/keep.d/daed-router
endef

define Package/luci-app-daed-router/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/daed-router
	$(INSTALL_DATA) ./htdocs/*.js $(1)/www/luci-static/resources/view/daed-router/
	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d $(1)/usr/share/rpcd/acl.d $(1)/usr/libexec/rpcd
	$(INSTALL_DATA) ./files/menu.json $(1)/usr/share/luci/menu.d/luci-app-daed-router.json
	$(INSTALL_DATA) ./files/acl.json $(1)/usr/share/rpcd/acl.d/luci-app-daed-router.json
	$(INSTALL_BIN) ./files/rpcd-daed $(1)/usr/libexec/rpcd/daed-router
	$(INSTALL_DIR) $(1)/www/cgi-bin
	$(INSTALL_BIN) ./files/luci-cgi $(1)/www/cgi-bin/daed-router
endef

define Package/luci-app-daed-router/postinst
#!/bin/sh
if [ -z "$$IPKG_INSTROOT" ]; then
	rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*.json
	rm -rf /tmp/luci-modulecache
	/etc/init.d/rpcd reload
fi
exit 0
endef

$(eval $(call BuildPackage,daed-router))
$(eval $(call BuildPackage,luci-app-daed-router))
