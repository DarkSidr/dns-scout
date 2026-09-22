include $(TOPDIR)/rules.mk

PKG_NAME:=dns-scout
PKG_VERSION:=$(shell cat $(CURDIR)/VERSION)
PKG_RELEASE:=$(shell cat $(CURDIR)/RELEASE)
PKG_LICENSE:=MIT
PKG_MAINTAINER:=DNS Scout contributors
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16
GO_PKG:=dns-scout
GO_PKG_BUILD_PKG:=dns-scout/cmd/dns-scout
GO_PKG_LDFLAGS:=-s -w
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)-r$(PKG_RELEASE)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/luci-app-dns-scout
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=DoH testing and verified resolver selection
  DEPENDS:=$(GO_ARCH_DEPENDS) +luci-base +rpcd +ca-bundle +https-dns-proxy
endef

define Package/luci-app-dns-scout/description
  Bounded DoH benchmarks, LuCI table, manual providers, cron scheduling,
  ordered fallbacks and transactional https-dns-proxy configuration.
endef

define Package/luci-app-dns-scout/conffiles
/etc/dns-scout/config.json
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) ./go.mod ./cmd $(PKG_BUILD_DIR)/
endef

define Package/luci-app-dns-scout/install
	$(CP) ./files/* $(1)/
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/dns-scout $(1)/usr/sbin/dns-scout
endef

define Package/luci-app-dns-scout/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] && exit 0
/etc/init.d/dns-scout enable
/etc/init.d/rpcd restart
exit 0
endef

define Package/luci-app-dns-scout/prerm
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] && exit 0
[ ! -f /etc/dns-scout/transaction/pending ] || exit 1
/etc/init.d/dns-scout disable
[ ! -f /etc/crontabs/root ] || sed -i '/ # dns-scout$$/d' /etc/crontabs/root
/etc/init.d/cron restart
exit 0
endef

$(eval $(call BuildPackage,luci-app-dns-scout))
